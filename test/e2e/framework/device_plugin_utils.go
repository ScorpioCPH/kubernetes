/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package framework

import (
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1alpha"
	imageutils "k8s.io/kubernetes/test/utils/image"
)

const (
	// Dummy device resource name to be advertised to Kubelet
	StubDeviceName = "dummy.device.com/device"
)

func NumberOfStubDevices(node *v1.Node) int64 {
	val, ok := node.Status.Capacity[StubDeviceName]

	if !ok {
		return 0
	}

	return val.Value()
}

// StubDevicePlugin returns the stub Device Plugin pod for conformance testing
func StubDevicePlugin(ns string) *v1.Pod {
	p := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stub-device-plugin-" + string(uuid.NewUUID()),
			Namespace: ns,
		},

		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyAlways,
			Volumes: []v1.Volume{{
				Name: "device-plugin-path",
				VolumeSource: v1.VolumeSource{
					HostPath: &v1.HostPathVolumeSource{
						Path: pluginapi.DevicePluginPath,
					},
				},
			}, {
				Name: "dummy-device-path",
				VolumeSource: v1.VolumeSource{
					HostPath: &v1.HostPathVolumeSource{
						Path: "/tmp/dummy-device",
					},
				},
			}},
			Containers: []v1.Container{
				{
					Name:  "stub-device-plugin",
					Image: imageutils.GetE2EImage(imageutils.StubDevicePlugin),
					Args:  []string{"-resource_name", StubDeviceName},
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "device-plugin-path",
							MountPath: pluginapi.DevicePluginPath,
						},
						{
							Name:      "dummy-device-path",
							MountPath: "/tmp/dummy-device",
						},
					},
				},
			},
		},
	}

	return p
}
