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
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1alpha"
	"k8s.io/kubernetes/test/e2e_node/builder"
	imageutils "k8s.io/kubernetes/test/utils/image"

	. "github.com/onsi/gomega"
)

const (
	// dummy device file saved in "/tmp/dummy-device" directory,
	// e.g. "/tmp/dummy-device/device-1", "/tmp/dummy-device/device-2"
	DummyDeviceDir string = "dummy-device"
	DummyDeviceRE  string = "^device-[0-9]*$"
)

func NumberOfStubDevices(node *v1.Node, resourceName string) int64 {
	val, ok := node.Status.Capacity[v1.ResourceName(resourceName)]

	if !ok {
		return 0
	}

	return val.Value()
}

// StubDevicePlugin returns the stub Device Plugin pod for conformance testing
func StubDevicePlugin(ns, resourcePath string) *v1.Pod {
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
					Args:  []string{"-resource-path", resourcePath},
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

// dummyDevice represents the dummy devices details with resource name.
type dummyDevice struct {
	ResourceName string             `json:"resourceName,omitempty"`
	Devices      []pluginapi.Device `json:"devices,omitempty"`
}

// dummyDeviceList is a list of dummyDevice objects.
type dummyDeviceList struct {
	Items []dummyDevice `json:"items,omitempty"`
}

// DummyResource represents the dummy resource details
type DummyResource struct {
	ResourceName string `json:"resourceName,omitempty"`
	ResourcePath string `json:"resourcePath,omitempty"`
}

// CreateDummyDeviceFileFromConfig reads config file and parse it to create dummy device files on node
// return array of DummyResource
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
func CreateDummyDeviceFileFromConfig() ([]*DummyResource, error) {
	Logf("CreateDummyDeviceFileFromConfig")
	// clean up dummy device dir if needed
	dir := path.Join(os.TempDir(), DummyDeviceDir)
	err := os.RemoveAll(dir)
	if err != nil {
		return nil, err
	}

	// create dummy device dir
	err = os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		return nil, err
	}

	// get dummy device list from config file
	dummyDeviceList, err := readConfigFile()
	if err != nil {
		return nil, err
	}

	// returned resources map (resourceName:resourcePath)
	resources := []*DummyResource{}
	for _, item := range dummyDeviceList.Items {
		// TODO(cph): hacky here, replace "/" with "-" to prevent the depth of the directory from increasing
		// create subdir with resource name
		resourcePath := path.Join(DummyDeviceDir, strings.Replace(item.ResourceName, "/", "-", -1))
		subdir := path.Join(os.TempDir(), resourcePath)
		err = os.MkdirAll(subdir, os.ModePerm)
		if err != nil {
			return nil, err
		}

		resources = append(resources, &DummyResource{
			ResourceName: item.ResourceName,
			ResourcePath: resourcePath,
		})

		for _, device := range item.Devices {
			// create dummy device file with health state and resource name
			f1, err := os.Create(filepath.Join(subdir, device.ID))
			defer f1.Close()
			if err != nil {
				return nil, err
			}

			_, err = f1.WriteString(device.Health)
			if err != nil {
				return nil, err
			}
		}

	}

	return resources, nil
}

func readConfigFile() (*dummyDeviceList, error) {
	// get k8s root dir
	rootDir, err := builder.GetK8sRootDir()
	if err != nil {
		Logf("failed to locate kubernetes root directory: %v", err)
		return nil, err
	}

	// read config file
	file, err := ioutil.ReadFile(filepath.Join(rootDir, "/test/images/stub-device-plugin/config.json"))
	if err != nil {
		Logf("failed to read config file: %v", err)
		return nil, err
	}

	var list dummyDeviceList
	err = json.Unmarshal(file, &list)
	if err != nil {
		Logf("failed to unmarshal config file: %v\n", err)
		return nil, err
	}

	return &list, nil
}

func NewDecimalResourceList(name v1.ResourceName, quantity int64) v1.ResourceList {
	return v1.ResourceList{name: *resource.NewQuantity(quantity, resource.DecimalSI)}
}

// TODO: Find a uniform way to deal with systemctl/initctl/service operations. #34494
func RestartKubeletWithSocketRE(re string) {
	beforeSocks, err := filepath.Glob(re)
	ExpectNoError(err)
	Expect(len(beforeSocks)).NotTo(BeZero())
	stdout, err := exec.Command("sudo", "systemctl", "list-units", "kubelet*", "--state=running").CombinedOutput()
	ExpectNoError(err)
	regex := regexp.MustCompile("(kubelet-[0-9]+)")
	matches := regex.FindStringSubmatch(string(stdout))
	Expect(len(matches)).NotTo(BeZero())
	kube := matches[0]
	Logf("Get running kubelet with systemctl: %v, %v", string(stdout), kube)
	stdout, err = exec.Command("sudo", "systemctl", "restart", kube).CombinedOutput()
	ExpectNoError(err, "Failed to restart kubelet with systemctl: %v, %v", err, stdout)
	Eventually(func() ([]string, error) {
		return filepath.Glob(re)
	}, 5*time.Minute, Poll).ShouldNot(ConsistOf(beforeSocks))
}

func GetDeviceIdWithRE(f *Framework, podName string, contName string, restartCount int32, re string) string {
	// Wait till pod has been restarted at least restartCount times.
	Eventually(func() bool {
		p, err := f.PodClient().Get(podName, metav1.GetOptions{})
		if err != nil || len(p.Status.ContainerStatuses) < 1 {
			return false
		}
		return p.Status.ContainerStatuses[0].RestartCount >= restartCount
	}, 5*time.Minute, Poll).Should(BeTrue())
	logs, err := GetPodLogs(f.ClientSet, f.Namespace.Name, podName, contName)
	if err != nil {
		Failf("GetPodLogs for pod %q failed: %v", podName, err)
	}
	Logf("got pod logs: %v", logs)
	regex := regexp.MustCompile(re)
	matches := regex.FindStringSubmatch(logs)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}
