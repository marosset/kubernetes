//go:build windows
// +build windows

/*
Copyright 2024 The Kubernetes Authors.

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
	"testing"

	"k8s.io/kubernetes/pkg/kubelet/winstats"
	"k8s.io/utils/cpuset"
)

func TestComputeCPUSet(t *testing.T) {
	affinities := []winstats.GROUP_AFFINITY{
		{Mask: 0b1010, Group: 0}, // CPUs 1 and 3 in Group 0
		{Mask: 0b1001, Group: 1}, // CPUs 0 and 3 in Group 1
	}

	expected := map[int]struct{}{
		1:  {}, // Group 0, CPU 1
		3:  {}, // Group 0, CPU 3
		64: {}, // Group 1, CPU 0
		67: {}, // Group 1, CPU 3
	}

	result := computeCPUSet(affinities)
	if len(result) != len(expected) {
		t.Errorf("expected length %v, but got length %v", len(expected), len(result))
	}
	for key := range expected {
		if _, exists := result[key]; !exists {
			t.Errorf("expected key %v to be in result", key)
		}
	}
}

func TestSubset(t *testing.T) {
	tests := []struct {
		set1     map[int]struct{}
		set2     map[int]struct{}
		expected bool
	}{
		{
			set1:     map[int]struct{}{1: {}, 2: {}},
			set2:     map[int]struct{}{1: {}, 2: {}, 3: {}},
			expected: true,
		},
		{
			set1:     map[int]struct{}{1: {}, 4: {}},
			set2:     map[int]struct{}{1: {}, 2: {}, 3: {}},
			expected: false,
		},
		{
			set1:     map[int]struct{}{},
			set2:     map[int]struct{}{1: {}, 2: {}, 3: {}},
			expected: true,
		},
		{
			set1:     map[int]struct{}{1: {}, 2: {}, 3: {}},
			set2:     map[int]struct{}{1: {}, 2: {}, 3: {}},
			expected: true,
		},
		{
			set1:     map[int]struct{}{1: {}, 2: {}, 3: {}},
			set2:     map[int]struct{}{1: {}, 2: {}},
			expected: false,
		},
	}

	for _, test := range tests {
		result := subset(test.set1, test.set2)
		if result != test.expected {
			t.Errorf("subset(%v, %v) = %v; expected %v", test.set1, test.set2, result, test.expected)
		}
	}
}

func TestMergeSets(t *testing.T) {
	tests := []struct {
		set1     map[int]struct{}
		set2     map[int]struct{}
		expected map[int]struct{}
	}{
		{
			set1:     map[int]struct{}{1: {}, 2: {}},
			set2:     map[int]struct{}{3: {}, 4: {}},
			expected: map[int]struct{}{1: {}, 2: {}, 3: {}, 4: {}},
		},
		{
			set1:     map[int]struct{}{1: {}, 2: {}},
			set2:     map[int]struct{}{2: {}, 3: {}},
			expected: map[int]struct{}{1: {}, 2: {}, 3: {}},
		},
		{
			set1:     map[int]struct{}{},
			set2:     map[int]struct{}{1: {}, 2: {}},
			expected: map[int]struct{}{1: {}, 2: {}},
		},
		{
			set1:     map[int]struct{}{1: {}, 2: {}},
			set2:     map[int]struct{}{},
			expected: map[int]struct{}{1: {}, 2: {}},
		},
		{
			set1:     map[int]struct{}{},
			set2:     map[int]struct{}{},
			expected: map[int]struct{}{},
		},
	}

	for _, test := range tests {
		result := mergeSets(test.set1, test.set2)
		if len(result) != len(test.expected) {
			t.Errorf("expected length %v, but got length %v", len(test.expected), len(result))
		}
		for key := range test.expected {
			if _, exists := result[key]; !exists {
				t.Errorf("expected key %v to be in result", key)
			}
		}
	}
}

func TestConvertToGroupAffinities(t *testing.T) {
	tests := []struct {
		cpuSet   cpuset.CPUSet
		expected []winstats.GROUP_AFFINITY
	}{
		{
			cpuSet: cpuset.New(0, 1, 2, 3),
			expected: []winstats.GROUP_AFFINITY{
				{Group: 0, Mask: 0b1111},
			},
		},
		{
			cpuSet: cpuset.New(64, 65, 66, 67),
			expected: []winstats.GROUP_AFFINITY{
				{Group: 1, Mask: 0b1111},
			},
		},
		{
			cpuSet: cpuset.New(0, 65),
			expected: []winstats.GROUP_AFFINITY{
				{Group: 0, Mask: 0b1},
				{Group: 1, Mask: 0b10},
			},
		},
		{
			cpuSet:   cpuset.New(),
			expected: []winstats.GROUP_AFFINITY{},
		},
	}

	for _, test := range tests {
		result := convertToGroupAffinities(test.cpuSet)
		if len(result) != len(test.expected) {
			t.Errorf("expected length %v, but got length %v", len(test.expected), len(result))
		}
		for i, expectedAffinity := range test.expected {
			if result[i].Group != expectedAffinity.Group || result[i].Mask != expectedAffinity.Mask {
				t.Errorf("expected affinity %v, but got affinity %v", expectedAffinity, result[i])
			}
		}
	}
}

func TestGroupMasks(t *testing.T) {
	tests := []struct {
		cpuSet   map[int]struct{}
		expected map[int]uint64
	}{
		{
			cpuSet: map[int]struct{}{
				0: {}, 1: {}, 2: {}, 3: {},
				64: {}, 65: {}, 66: {}, 67: {},
			},
			expected: map[int]uint64{
				0: 0b1111,
				1: 0b1111,
			},
		},
		{
			cpuSet: map[int]struct{}{
				0: {}, 2: {}, 64: {}, 66: {},
			},
			expected: map[int]uint64{
				0: 0b0101,
				1: 0b0101,
			},
		},
		{
			cpuSet: map[int]struct{}{
				1: {}, 65: {},
			},
			expected: map[int]uint64{
				0: 0b0010,
				1: 0b0010,
			},
		},
		{
			cpuSet:   map[int]struct{}{},
			expected: map[int]uint64{},
		},
	}

	for _, test := range tests {
		result := groupMasks(test.cpuSet)
		if len(result) != len(test.expected) {
			t.Errorf("expected length %v, but got length %v", len(test.expected), len(result))
		}
		for group, mask := range test.expected {
			if result[group] != mask {
				t.Errorf("expected group %v to have mask %v, but got mask %v", group, mask, result[group])
			}
		}
	}
}
