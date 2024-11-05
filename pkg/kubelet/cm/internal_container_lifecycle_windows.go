//go:build windows
// +build windows

/*
Copyright 2020 The Kubernetes Authors.

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

package cm

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	"k8s.io/klog/v2"
	kubefeatures "k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/kubelet/winstats"
	"k8s.io/utils/cpuset"
)

func (i *internalContainerLifecycleImpl) PreCreateContainer(pod *v1.Pod, container *v1.Container, containerConfig *runtimeapi.ContainerConfig) error {
	if !utilfeature.DefaultFeatureGate.Enabled(kubefeatures.WindowsCPUAndMemoryAffinity) {
		return nil
	}

	klog.Info("PreCreateContainer for Windows")

	// retrieve CPU and NUMA affinity from CPU Manager and Memory Manager (if enabled)
	var allocatedCPUs cpuset.CPUSet
	if i.cpuManager != nil {
		allocatedCPUs = i.cpuManager.GetCPUAffinity(string(pod.UID), container.Name)
	}

	var numaNodes sets.Set[int]
	if i.memoryManager != nil {
		numaNodes = i.memoryManager.GetMemoryNUMANodes(pod, container)
	}

	// set affinity based on available managers
	var finalCPUSet map[int]struct{}

	if i.cpuManager != nil && !allocatedCPUs.IsEmpty() && i.memoryManager != nil && numaNodes.Len() > 0 {
		// Both CPU and memory managers are enabled
		klog.V(4).Infof("CPU manager selected CPUS %v, memory manager selected NUMA nodes %v for container %v pod %v", numaNodes, allocatedCPUs, container.Name, pod.UID)

		// Gather all CPUs associated with the selected NUMA nodes
		var allNumaNodeCPUs []winstats.GROUP_AFFINITY
		for _, numaNode := range sets.List(numaNodes) {
			affinity, err := winstats.GetCPUsforNUMANode(uint16(numaNode))
			if err != nil {
				return fmt.Errorf("failed to get CPUs for NUMA node %d: %v", numaNode, err)
			}
			allNumaNodeCPUs = append(allNumaNodeCPUs, *affinity)
		}

		// Create sets from integer representation of CPUs ((group * 64) + processorId for each bit set in the affinity mask)
		cpuManagerAffinityCPUSet := computeCPUSet(convertToGroupAffinities(allocatedCPUs))
		numaNodeAffinityCPUSet := computeCPUSet(allNumaNodeCPUs)

		// Determine which set of CPUs to use using the following logic outlined in the KEP:
		// Case 1: CPU manager selects more CPUs than those availble in the NUMA nodes selected by the memory manager
		// Case 2: CPU manager selects fewer CPUs, and they all fall within the CPUs available in the NUMA nodes selected by the memory manager
		// Case 3: CPU manager selects fewer CPUs, but some are outside of the CPUs available in the NUMA nodes selected by the memory manager

		if len(cpuManagerAffinityCPUSet) > len(numaNodeAffinityCPUSet) {
			// Case 1, use CPU manager selected CPUs
			finalCPUSet = cpuManagerAffinityCPUSet
		} else if subset(cpuManagerAffinityCPUSet, numaNodeAffinityCPUSet) {
			// case 2, use CPU manager selected CPUs
			finalCPUSet = cpuManagerAffinityCPUSet
		} else {
			// Case 3, merge CPU manager and memory manager selected CPUs
			finalCPUSet = mergeSets(cpuManagerAffinityCPUSet, numaNodeAffinityCPUSet)
		}
	} else if i.cpuManager != nil && !allocatedCPUs.IsEmpty() {
		// Only CPU manager is enabled, use CPU manager selected CPUs
		finalCPUSet = computeCPUSet(convertToGroupAffinities(allocatedCPUs))
	} else if i.memoryManager != nil && !allocatedCPUs.IsEmpty() {
		// Only memory manager is enabled, use CPUs associated with selected NUMA nodes
		var allNumaNodeCPUs []winstats.GROUP_AFFINITY
		for _, numaNode := range sets.List(numaNodes) {
			affinity, err := winstats.GetCPUsforNUMANode(uint16(numaNode))
			if err != nil {
				return fmt.Errorf("failed to get CPUs for NUMA node %d: %v", numaNode, err)
			}
			allNumaNodeCPUs = append(allNumaNodeCPUs, *affinity)
		}
		finalCPUSet = computeCPUSet(allNumaNodeCPUs)
	}

	// Set CPU group affinities in the container config
	if finalCPUSet != nil {
		var cpusToGroupAffinities []*runtimeapi.WindowsCpuGroupAffinity
		for group, mask := range groupMasks(finalCPUSet) {
			cpusToGroupAffinities = append(cpusToGroupAffinities, &runtimeapi.WindowsCpuGroupAffinity{
				CpuGroup: uint32(group),
				CpuMask:  uint64(mask),
			})
		}
		containerConfig.Windows.Resources.AffinityCpus = cpusToGroupAffinities
	}

	// return nil if no CPUs were selected
	return nil
}

// computeCPUSet returns a map of CPU IDs to an empty struct based on the provided group affinities
func computeCPUSet(affinities []winstats.GROUP_AFFINITY) map[int]struct{} {
	cpuSet := make(map[int]struct{})
	for _, affinity := range affinities {
		for processorID := range affinity.Processors() {
			cpuSet[processorID] = struct{}{}
		}
	}
	return cpuSet
}

// subset returns true if set1 is a subset of set2
func subset(set1, set2 map[int]struct{}) bool {
	for k := range set1 {
		if _, ok := set2[k]; !ok {
			return false
		}
	}
	return true
}

// mergeSets combines two sets of CPU IDs
func mergeSets(set1, set2 map[int]struct{}) map[int]struct{} {
	mergedSet := make(map[int]struct{})
	for k := range set1 {
		mergedSet[k] = struct{}{}
	}
	for k := range set2 {
		mergedSet[k] = struct{}{}
	}
	return mergedSet
}

// convertToGroupAffinities converts a cpuset.CPUSet to a slice of winstats.GROUP_AFFINITY
func convertToGroupAffinities(cpuSet cpuset.CPUSet) []winstats.GROUP_AFFINITY {
	var affinities []winstats.GROUP_AFFINITY
	for _, cpu := range cpuSet.List() {
		group := cpu / 64
		processor := cpu % 64
		affinity := winstats.GROUP_AFFINITY{
			Group:    uint16(group),
			Mask:     1 << processor,
			Reserved: [3]uint16{},
		}
		affinities = append(affinities, affinity)
	}
	return affinities
}

// groupMasks converts a set of CPU IDs into group and mask representations
func groupMasks(cpuSet map[int]struct{}) map[int]uint64 {
	groupMasks := make(map[int]uint64)
	for cpu := range cpuSet {
		group := cpu / 64
		mask := uint64(1) << (cpu % 64)
		groupMasks[group] |= mask
	}
	return groupMasks
}
