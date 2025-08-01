// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package allocation

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/open-telemetry/opentelemetry-operator/cmd/otel-allocator/internal/diff"
	"github.com/open-telemetry/opentelemetry-operator/cmd/otel-allocator/internal/target"
)

/*
	Target Allocator will serve on an HTTP server exposing /jobs/<job_id>/targets
	The targets are allocated using the least connection method
	Target Allocator will need information about the collectors in order to set the URLs
	Keep a Map of what each collector currently holds and update it based on new scrape target updates
*/

var _ Allocator = &allocator{}

func newAllocator(log logr.Logger, strategy Strategy, opts ...Option) (Allocator, error) {
	meter := otel.GetMeterProvider().Meter("targetallocator")
	// targetsPerCollector records how many targets have been assigned to each collector.
	// It is currently the responsibility of the strategy to track this information.
	targetsPerCollector, err := meter.Int64Gauge("opentelemetry_allocator_targets_per_collector", metric.WithDescription("The number of targets for each collector."))
	if err != nil {
		return nil, err
	}
	collectorsAllocatable, err := meter.Int64Gauge("opentelemetry_allocator_collectors_allocatable", metric.WithDescription("Number of collectors the allocator is able to allocate to."))
	if err != nil {
		return nil, err
	}
	timeToAssign, err := meter.Float64Histogram("opentelemetry_allocator_time_to_allocate", metric.WithDescription("The time it takes to allocate"))
	if err != nil {
		return nil, err
	}
	targetsRemaining, err := meter.Int64Gauge("opentelemetry_allocator_targets_remaining", metric.WithDescription("Number of targets kept after filtering."))
	if err != nil {
		return nil, err
	}
	targetsUnassigned, err := meter.Int64Gauge("opentelemetry_allocator_targets_unassigned", metric.WithDescription("Number of targets that could not be assigned due to missing node label."))
	if err != nil {
		return nil, err
	}

	chAllocator := &allocator{
		strategy:                      strategy,
		collectors:                    make(map[string]*Collector),
		targetItems:                   make(map[target.ItemHash]*target.Item),
		targetItemsPerJobPerCollector: make(map[string]map[string]map[target.ItemHash]bool),
		log:                           log,
		targetsPerCollector:           targetsPerCollector,
		collectorsAllocatable:         collectorsAllocatable,
		timeToAssign:                  timeToAssign,
		targetsRemaining:              targetsRemaining,
		targetsUnassigned:             targetsUnassigned,
	}
	for _, opt := range opts {
		opt(chAllocator)
	}

	return chAllocator, nil
}

type allocator struct {
	strategy Strategy

	// collectors is a map from a Collector's name to a Collector instance
	// collectorKey -> collector pointer
	collectors map[string]*Collector

	// targetItems is a map from a target item's hash to the target items allocated state
	// targetItem hash -> target item pointer
	targetItems map[target.ItemHash]*target.Item

	// collectorKey -> job -> target item hash -> true
	targetItemsPerJobPerCollector map[string]map[string]map[target.ItemHash]bool

	// m protects collectors, targetItems and targetItemsPerJobPerCollector for concurrent use.
	m sync.RWMutex

	log logr.Logger

	filter                Filter
	targetsPerCollector   metric.Int64Gauge
	collectorsAllocatable metric.Int64Gauge
	timeToAssign          metric.Float64Histogram
	targetsRemaining      metric.Int64Gauge
	targetsUnassigned     metric.Int64Gauge
}

// SetFilter sets the filtering hook to use.
func (a *allocator) SetFilter(filter Filter) {
	a.filter = filter
}

// SetFallbackStrategy sets the fallback strategy to use.
func (a *allocator) SetFallbackStrategy(strategy Strategy) {
	a.strategy.SetFallbackStrategy(strategy)
}

// SetTargets accepts a list of targets that will be used to make
// load balancing decisions. This method should be called when there are
// new targets discovered or existing targets are shutdown.
func (a *allocator) SetTargets(targets []*target.Item) {
	begin := time.Now()
	defer func() {
		a.timeToAssign.Record(context.Background(), time.Since(begin).Seconds(), metric.WithAttributes(attribute.String("method", "SetTargets"), attribute.String("strategy", a.strategy.GetName())))
	}()

	if a.filter != nil {
		targets = a.filter.Apply(targets)
	}

	a.targetsRemaining.Record(context.Background(), int64(len(targets)))
	concurrency := runtime.NumCPU() * 2 // determined experimentally
	targetMap := buildTargetMap(targets, concurrency)

	a.m.Lock()
	defer a.m.Unlock()

	// Check for target changes
	targetsDiff := diff.Maps(a.targetItems, targetMap)
	// If there are any additions or removals
	if len(targetsDiff.Additions()) != 0 || len(targetsDiff.Removals()) != 0 {
		a.handleTargets(targetsDiff)
	}
}

// SetCollectors sets the set of collectors with key=collectorName, value=Collector object.
// This method is called when Collectors are added or removed.
func (a *allocator) SetCollectors(collectors map[string]*Collector) {
	begin := time.Now()
	defer func() {
		a.timeToAssign.Record(context.Background(), time.Since(begin).Seconds(), metric.WithAttributes(attribute.String("strategy", a.strategy.GetName()), attribute.String("method", "SetCollectors")))
	}()

	a.collectorsAllocatable.Record(context.Background(), int64(len(collectors)), metric.WithAttributes(attribute.String("strategy", a.strategy.GetName())))
	if len(collectors) == 0 {
		a.log.Info("No collector instances present")
	}

	a.m.Lock()
	defer a.m.Unlock()

	// Check for collector changes
	collectorsDiff := diff.Maps(a.collectors, collectors)
	if len(collectorsDiff.Additions()) != 0 || len(collectorsDiff.Removals()) != 0 {
		a.handleCollectors(collectorsDiff)
	}
}

func (a *allocator) GetTargetsForCollectorAndJob(collector string, job string) []*target.Item {
	a.m.RLock()
	defer a.m.RUnlock()
	if _, ok := a.targetItemsPerJobPerCollector[collector]; !ok {
		return []*target.Item{}
	}
	if _, ok := a.targetItemsPerJobPerCollector[collector][job]; !ok {
		return []*target.Item{}
	}
	targetItemsCopy := make([]*target.Item, len(a.targetItemsPerJobPerCollector[collector][job]))
	index := 0
	for targetHash := range a.targetItemsPerJobPerCollector[collector][job] {
		targetItemsCopy[index] = a.targetItems[targetHash]
		index++
	}
	return targetItemsCopy
}

// TargetItems returns a shallow copy of the targetItems map.
// The key is the target item's hash, and the value is the target item.
func (a *allocator) TargetItems() map[target.ItemHash]*target.Item {
	a.m.RLock()
	defer a.m.RUnlock()
	targetItemsCopy := make(map[target.ItemHash]*target.Item)
	for k, v := range a.targetItems {
		targetItemsCopy[k] = v
	}
	return targetItemsCopy
}

// Collectors returns a shallow copy of the collectors map.
func (a *allocator) Collectors() map[string]*Collector {
	a.m.RLock()
	defer a.m.RUnlock()
	collectorsCopy := make(map[string]*Collector)
	for k, v := range a.collectors {
		collectorsCopy[k] = v
	}
	return collectorsCopy
}

// handleTargets receives the new and removed targets and reconciles the current state.
// Any removals are removed from the allocator's targetItems and unassigned from the corresponding collector.
// Any net-new additions are assigned to the collector on the same node as the target.
func (a *allocator) handleTargets(diff diff.Changes[target.ItemHash, *target.Item]) {
	// Check for removals
	for k, item := range a.targetItems {
		// if the current item is in the removals list
		if _, ok := diff.Removals()[k]; ok {
			a.removeTargetItem(item)
		}
	}

	// Check for additions
	var assignmentErrors []error
	for k, item := range diff.Additions() {
		// Do nothing if the item is already there
		if _, ok := a.targetItems[k]; ok {
			continue
		} else {
			// TODO: track target -> collector relationship in a separate map
			item.CollectorName = ""
			// Add item to item pool and assign a collector
			err := a.addTargetToTargetItems(item)
			if err != nil {
				assignmentErrors = append(assignmentErrors, err)
			}
		}
	}

	// Check for unassigned targets
	unassignedTargets := len(assignmentErrors)
	if unassignedTargets > 0 {
		err := errors.Join(assignmentErrors...)
		a.log.Info("Could not assign targets for some jobs", "targets", unassignedTargets, "error", err)
		a.targetsUnassigned.Record(context.Background(), int64(unassignedTargets))
	}
}

func (a *allocator) addTargetToTargetItems(tg *target.Item) error {
	a.targetItems[tg.Hash()] = tg
	if len(a.collectors) == 0 {
		return nil
	}

	colOwner, err := a.strategy.GetCollectorForTarget(a.collectors, tg)
	if err != nil {
		return err
	}

	// Check if this is a reassignment, if so, unassign first
	// note: The ordering here is important, we want to determine the new assignment before unassigning, because
	// the strategy might make use of previous assignment information
	if _, ok := a.collectors[tg.CollectorName]; ok && tg.CollectorName != "" {
		a.unassignTargetItem(tg)
	}

	tg.CollectorName = colOwner.Name
	a.addCollectorTargetItemMapping(tg)
	a.collectors[colOwner.Name].NumTargets++
	a.targetsPerCollector.Record(context.Background(), int64(a.collectors[colOwner.String()].NumTargets), metric.WithAttributes(attribute.String("collector_name", colOwner.String()), attribute.String("strategy", a.strategy.GetName())))
	return nil
}

// unassignTargetItem unassigns the target item from its Collector. The target item is still tracked.
func (a *allocator) unassignTargetItem(item *target.Item) {
	collectorName := item.CollectorName
	if collectorName == "" {
		return
	}
	c, ok := a.collectors[collectorName]
	if !ok {
		return
	}
	c.NumTargets--
	a.targetsPerCollector.Record(context.Background(), int64(c.NumTargets), metric.WithAttributes(attribute.String("collector_name", item.CollectorName), attribute.String("strategy", a.strategy.GetName())))
	delete(a.targetItemsPerJobPerCollector[item.CollectorName][item.JobName], item.Hash())
	if len(a.targetItemsPerJobPerCollector[item.CollectorName][item.JobName]) == 0 {
		delete(a.targetItemsPerJobPerCollector[item.CollectorName], item.JobName)
	}
	item.CollectorName = ""
}

// removeTargetItem removes the target item from its Collector.
func (a *allocator) removeTargetItem(item *target.Item) {
	a.unassignTargetItem(item)
	delete(a.targetItems, item.Hash())
}

// removeCollector removes a Collector from the allocator.
func (a *allocator) removeCollector(collector *Collector) {
	delete(a.collectors, collector.Name)
	// Remove the collector from any target item records
	for _, targetItems := range a.targetItemsPerJobPerCollector[collector.Name] {
		for targetHash := range targetItems {
			a.targetItems[targetHash].CollectorName = ""
		}
	}
	delete(a.targetItemsPerJobPerCollector, collector.Name)
	a.targetsPerCollector.Record(context.Background(), 0, metric.WithAttributes(attribute.String("collector_name", collector.Name), attribute.String("strategy", a.strategy.GetName())))
}

// addCollectorTargetItemMapping keeps track of which collector has which jobs and targets
// this allows the allocator to respond without any extra allocations to http calls. The caller of this method
// has to acquire a lock.
func (a *allocator) addCollectorTargetItemMapping(tg *target.Item) {
	if a.targetItemsPerJobPerCollector[tg.CollectorName] == nil {
		a.targetItemsPerJobPerCollector[tg.CollectorName] = make(map[string]map[target.ItemHash]bool)
	}
	if a.targetItemsPerJobPerCollector[tg.CollectorName][tg.JobName] == nil {
		a.targetItemsPerJobPerCollector[tg.CollectorName][tg.JobName] = make(map[target.ItemHash]bool)
	}
	a.targetItemsPerJobPerCollector[tg.CollectorName][tg.JobName][tg.Hash()] = true
}

// handleCollectors receives the new and removed collectors and reconciles the current state.
// Any removals are removed from the allocator's collectors. New collectors are added to the allocator's collector map.
// Finally, update all targets' collector assignments.
func (a *allocator) handleCollectors(diff diff.Changes[string, *Collector]) {
	// Clear removed collectors
	for _, k := range diff.Removals() {
		a.removeCollector(k)
	}
	// Insert the new collectors
	for _, i := range diff.Additions() {
		a.collectors[i.Name] = NewCollector(i.Name, i.NodeName)
	}

	// Set collectors on the strategy
	a.strategy.SetCollectors(a.collectors)

	// Re-Allocate all targets
	var assignmentErrors []error
	for _, item := range a.targetItems {
		err := a.addTargetToTargetItems(item)
		if err != nil {
			assignmentErrors = append(assignmentErrors, err)
			item.CollectorName = ""
		}
	}
	// Check for unassigned targets
	unassignedTargets := len(assignmentErrors)
	if unassignedTargets > 0 {
		err := errors.Join(assignmentErrors...)
		a.log.Info("Could not assign targets for some jobs", "targets", unassignedTargets, "error", err)
		a.targetsUnassigned.Record(context.Background(), int64(unassignedTargets))
	}
}

const minChunkSize = 100 // for small target counts, it's not worth it to spawn a lot of goroutines

// buildTargetMap builds a map of targets, using their hashes as keys. It does this concurrently, and the concurrency
// is configurable via the concurrency parameter. We do this in parallel because target hashing is surprisingly
// expensive.
func buildTargetMap(targets []*target.Item, concurrency int) map[target.ItemHash]*target.Item {
	// technically there may be duplicates, so this may overallocate, but in the majority of cases it will be exact
	result := make(map[target.ItemHash]*target.Item, len(targets))
	chunkSize := len(targets) / concurrency
	chunkSize = max(chunkSize, minChunkSize)
	wg := sync.WaitGroup{}
	for chunk := range slices.Chunk(targets, chunkSize) {
		wg.Add(1)
		go func(ch []*target.Item) {
			defer wg.Done()
			for _, item := range ch {
				item.Hash()
			}
		}(chunk)
	}
	wg.Wait()
	for _, item := range targets {
		result[item.Hash()] = item
	}
	return result
}
