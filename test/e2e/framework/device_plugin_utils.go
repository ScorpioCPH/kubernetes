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
	"encoding/json"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1alpha"
	"k8s.io/kubernetes/test/e2e_node/builder"
	imageutils "k8s.io/kubernetes/test/utils/image"
)

const (
	// dummy device file saved in "/tmp/dummy-device" directory,
	// e.g. "/tmp/dummy-device/device-1", "/tmp/dummy-device/device-2"
	DummyDeviceDir string = "dummy-device"
	DummyDeviceRE  string = "^device-[0-9]*$"
)

var (
	// Dummy device resource name to be advertised to Kubelet
	ResourceName = "resource.name/1"
)

func NumberOfStubDevices(node *v1.Node) int64 {
	val, ok := node.Status.Capacity[v1.ResourceName(ResourceName)]

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
					// Args:  []string{"-resource_name", ResourceName},
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

// DummyDevice represents the dummy devices details with resource name.
type DummyDevice struct {
	ResourceName string             `json:"resourceName,omitempty"`
	Devices      []pluginapi.Device `json:"devices,omitempty"`
}

// DummyDeviceList is a list of DummyDevice objects.
type DummyDeviceList struct {
	Items []DummyDevice `json:"items,omitempty"`
}

// CreateDummyDeviceFileFromConfig reads config file and parse it to create dummy device files on node
// dummy device directory looks like this:
// |-- tmp
//     |-- dummy-device
//         |-- resource.name-1  # hacky here, replace "-" with "/" to follow kubernetes resource naming convention
//             |-- device-1 (content: healthy)
//             |-- device-2 (content: unhealthy)
//         |-- resource.name-2
//             |-- device-1 (content: healthy)
//             |-- device-2 (content: healthy)
//             |-- device-3 (content: unhealthy)
//         |-- resource.name-3
//             |-- device-1 (content: healthy)
//             |-- device-2 (content: healthy)
func CreateDummyDeviceFileFromConfig() error {
	// clean up dummy device dir if needed
	dir := path.Join(os.TempDir(), DummyDeviceDir)
	err := os.RemoveAll(dir)
	if err != nil {
		return err
	}

	err = os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		return err
	}

	dummyDeviceList := readConfigFile()

	for i, item := range dummyDeviceList.Items {
		if i == 0 {
			// TODO(cph): only get the first resource name now,
			// we can add more test by extending this
			ResourceName = item.ResourceName
		}

		// TODO(cph): hacky here, replace "/" with "-" to prevent the depth of the directory from increasing
		// create subdir with resource name
		subdir := path.Join(dir, strings.Replace(item.ResourceName, "/", "-", -1))
		err = os.MkdirAll(subdir, os.ModePerm)
		if err != nil {
			return err
		}

		for _, device := range item.Devices {
			// create dummy device file with health state and resource name
			f1, err := os.Create(filepath.Join(subdir, device.ID))
			defer f1.Close()
			if err != nil {
				return err
			}

			_, err = f1.WriteString(device.Health)
			if err != nil {
				return err
			}
		}

	}

	return nil
}

func readConfigFile() *DummyDeviceList {
	// hardcode default dummy device list
	dafaultList := DummyDeviceList{
		Items: []DummyDevice{
			{
				Devices: []pluginapi.Device{
					{
						ID:     "device-1",
						Health: pluginapi.Healthy,
					},
					{
						ID:     "device-2",
						Health: pluginapi.Healthy,
					},
				},
				ResourceName: ResourceName,
			},
		},
	}

	// get k8s root dir
	rootDir, err := builder.GetK8sRootDir()
	if err != nil {
		Logf("failed to locate kubernetes root directory: %v", err)
		return &dafaultList
	}

	// read config file
	file, err := ioutil.ReadFile(filepath.Join(rootDir, "/test/images/stub-device-plugin/config.json"))
	if err != nil {
		Logf("failed to read config file: %v", err)
		return &dafaultList
	}

	var dummyDeviceList DummyDeviceList
	err = json.Unmarshal(file, &dummyDeviceList)
	if err != nil {
		Logf("failed to unmarshal config file: %v\n", err)
		return &dafaultList
	}

	return &dummyDeviceList
}
