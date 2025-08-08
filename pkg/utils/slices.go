/*
Copyright 2023 Red Hat, Inc.

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

package utils

import "os"

// Find returns the first entry matching the function "f" or else return nil
func Find[T any](list []T, f func(item *T) bool) *T {
	for idx := range list {
		ele := &list[idx]
		if f(ele) {
			return ele
		}
	}
	return nil
}

func AssertEqual[T comparable](actual T, expected T, exitCode int) {
	if actual != expected {
		os.Exit(exitCode)
	}
}

func Filter[T any](s []T, predicate func(*T) bool) []T {
	result := make([]T, 0, len(s))
	for _, v := range s {
		if predicate(&v) {
			result = append(result, v)
		}
	}
	return result
}
