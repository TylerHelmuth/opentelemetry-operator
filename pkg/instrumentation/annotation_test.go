// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package instrumentation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEffectiveAnnotationValue(t *testing.T) {
	for _, tt := range []struct {
		desc     string
		expected string
		pod      corev1.Pod
		ns       corev1.Namespace
	}{
		{
			"pod-true-overrides-ns",
			"true",
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJava: "true",
					},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJava: "false",
					},
				},
			},
		},

		{
			"ns-has-concrete-instance",
			"some-instance",
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJava: "true",
					},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJava: "some-instance",
					},
				},
			},
		},

		{
			"pod-has-concrete-instance",
			"some-instance-from-pod",
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJava: "some-instance-from-pod",
					},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJava: "some-instance",
					},
				},
			},
		},

		{
			"pod-has-explicit-false",
			"false",
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJava: "false",
					},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJava: "some-instance",
					},
				},
			},
		},

		{
			"pod-has-no-annotations",
			"some-instance",
			corev1.Pod{},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJava: "some-instance",
					},
				},
			},
		},

		{
			"ns-has-no-annotations",
			"true",
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJava: "true",
					},
				},
			},
			corev1.Namespace{},
		},
	} {
		t.Run(tt.desc, func(t *testing.T) {
			// test
			annValue := annotationValue(tt.ns.ObjectMeta, tt.pod.ObjectMeta, annotationInjectJava)

			// verify
			assert.Equal(t, tt.expected, annValue)
		})
	}
}

func Test_hasLangSpecificContainers(t *testing.T) {
	for _, tt := range []struct {
		desc     string
		expected bool
		pod      corev1.Pod
		ns       corev1.Namespace
	}{
		{
			"no language specific containers",
			false,
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectContainerName: "myapp",
					},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
		},
		{
			"java container",
			true,
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJavaContainerName: "myapp",
					},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
		},
		{
			"nodejs container",
			true,
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectNodeJSContainerName: "myapp",
					},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
		},
		{
			"python container",
			true,
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectPythonContainerName: "myapp",
					},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
		},
		{
			"dotnet container",
			true,
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectDotNetContainerName: "myapp",
					},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
		},
		{
			"sdk container",
			true,
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectSDKContainerName: "myapp",
					},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
		},
		{
			"java ns",
			true,
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJavaContainerName: "myapp",
					},
				},
			},
		},
		{
			"nodejs ns",
			true,
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectNodeJSContainerName: "myapp",
					},
				},
			},
		},
		{
			"python ns",
			true,
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectPythonContainerName: "myapp",
					},
				},
			},
		},
		{
			"dotnet ns",
			true,
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectDotNetContainerName: "myapp",
					},
				},
			},
		},
		{
			"sdk ns",
			true,
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectSDKContainerName: "myapp",
					},
				},
			},
		},
		{
			"multiple",
			true,
			corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectJavaContainerName:   "myjavaapp",
						annotationInjectPythonContainerName: "mypythonapp",
					},
				},
			},
			corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationInjectDotNetContainerName: "mydotnetapp",
						annotationInjectSDKContainerName:    "mysdkapp",
					},
				},
			},
		},
	} {
		t.Run(tt.desc, func(t *testing.T) {
			// test
			annValue := hasLangSpecificContainers(tt.ns.ObjectMeta, tt.pod.ObjectMeta)

			// verify
			assert.Equal(t, tt.expected, annValue)
		})
	}
}
