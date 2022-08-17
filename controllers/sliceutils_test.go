/*
Copyright 2022 Red Hat, Inc.

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

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSliceContains(t *testing.T) {

	testCases := []struct {
		label       string
		slice       []string
		findString  string
		isContained bool
	}{
		{
			label:       "string exists in slice",
			slice:       []string{"foo", "bar"},
			findString:  "bar",
			isContained: true,
		},
		{
			label:       "string not in slice",
			slice:       []string{"foo", "bar"},
			findString:  "baz",
			isContained: false,
		},
		{
			label:       "empty string not in slice",
			slice:       []string{"foo", "bar"},
			findString:  "",
			isContained: false,
		},
		{
			label:       "string not in empty slice",
			slice:       []string{},
			findString:  "foo",
			isContained: false,
		},
	}

	for i, tc := range testCases {
		t.Logf("Case %d: %s\n", i+1, tc.label)
		checkContain := contains(tc.slice, tc.findString)
		assert.Equal(t, tc.isContained, checkContain)
	}
}

func TestSliceRemove(t *testing.T) {

	testCases := []struct {
		label         string
		slice         []string
		findString    string
		expectedSlice []string
	}{
		{
			label:         "string exists in slice",
			slice:         []string{"foo", "bar"},
			findString:    "foo",
			expectedSlice: []string{"bar"},
		},
		{
			label:         "string not in slice",
			slice:         []string{"foo", "bar"},
			findString:    "baz",
			expectedSlice: []string{"foo", "bar"},
		},
		{
			label:         "empty string not in slice",
			slice:         []string{"foo", "bar"},
			findString:    "",
			expectedSlice: []string{"foo", "bar"},
		},
		{
			label:         "string not in empty slice",
			slice:         []string{},
			findString:    "foo",
			expectedSlice: []string{},
		},
	}

	for i, tc := range testCases {
		t.Logf("Case %d: %s\n", i+1, tc.label)
		changedSlice := remove(tc.slice, tc.findString)
		assert.Equal(t, tc.expectedSlice, changedSlice)
	}
}
