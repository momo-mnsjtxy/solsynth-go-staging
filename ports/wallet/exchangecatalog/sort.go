// SPDX-License-Identifier: MIT
// Go translation of .NET Foundation ArraySortHelper<T> introsort, v10.0.0.
// Copyright (c) .NET Foundation and Contributors. See upstream/LICENSE-MIT.txt.
package exchangecatalog

import "math/bits"

// Framework-compatible unstable sorting is intentional: equal case-insensitive
// child keys from layered providers must not be reordered using a different
// sorting algorithm before GetChildren's distinct operation.
func sortConfigurationKeys(a []string) {
	if len(a) > 1 {
		introSort(a, 2*bits.Len(uint(len(a))))
	}
}
func swapGreater(a []string, i, j int) {
	if configKeyCompare(a[i], a[j]) > 0 {
		a[i], a[j] = a[j], a[i]
	}
}
func introSort(a []string, depth int) {
	for len(a) > 1 {
		if len(a) <= 16 {
			if len(a) == 2 {
				swapGreater(a, 0, 1)
				return
			}
			if len(a) == 3 {
				swapGreater(a, 0, 1)
				swapGreater(a, 0, 2)
				swapGreater(a, 1, 2)
				return
			}
			for i := 0; i < len(a)-1; i++ {
				v := a[i+1]
				j := i
				for j >= 0 && configKeyCompare(v, a[j]) < 0 {
					a[j+1] = a[j]
					j--
				}
				a[j+1] = v
			}
			return
		}
		if depth == 0 {
			heapSort(a)
			return
		}
		depth--
		hi := len(a) - 1
		mid := hi >> 1
		swapGreater(a, 0, mid)
		swapGreater(a, 0, hi)
		swapGreater(a, mid, hi)
		pivot := a[mid]
		a[mid], a[hi-1] = a[hi-1], a[mid]
		left, right := 0, hi-1
		for left < right {
			left++
			for configKeyCompare(a[left], pivot) < 0 {
				left++
			}
			right--
			for configKeyCompare(pivot, a[right]) < 0 {
				right--
			}
			if left >= right {
				break
			}
			a[left], a[right] = a[right], a[left]
		}
		if left != hi-1 {
			a[left], a[hi-1] = a[hi-1], a[left]
		}
		introSort(a[left+1:], depth)
		a = a[:left]
	}
}
func heapSort(a []string) {
	n := len(a)
	for i := n >> 1; i >= 1; i-- {
		downHeap(a, i, n)
	}
	for i := n; i > 1; i-- {
		a[0], a[i-1] = a[i-1], a[0]
		downHeap(a, 1, i-1)
	}
}
func downHeap(a []string, i, n int) {
	d := a[i-1]
	for i <= n>>1 {
		child := 2 * i
		if child < n && configKeyCompare(a[child-1], a[child]) < 0 {
			child++
		}
		if configKeyCompare(d, a[child-1]) >= 0 {
			break
		}
		a[i-1] = a[child-1]
		i = child
	}
	a[i-1] = d
}
