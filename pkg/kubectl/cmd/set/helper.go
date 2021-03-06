/*
Copyright 2016 The Kubernetes Authors.

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

package set

import (
	"fmt"
	"io"
	"strings"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/errors"
	kcmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/pkg/kubectl/resource"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/util/strategicpatch"
)

// selectContainers allows one or more containers to be matched against a string or wildcard
func selectContainers(containers []api.Container, spec string) ([]*api.Container, []*api.Container) {
	out := []*api.Container{}
	skipped := []*api.Container{}
	for i, c := range containers {
		if selectString(c.Name, spec) {
			out = append(out, &containers[i])
		} else {
			skipped = append(skipped, &containers[i])
		}
	}
	return out, skipped
}

// handlePodUpdateError prints a more useful error to the end user when mutating a pod.
func handlePodUpdateError(out io.Writer, err error, resource string) {
	if statusError, ok := err.(*errors.StatusError); ok && errors.IsInvalid(err) {
		errorDetails := statusError.Status().Details
		if errorDetails.Kind == "Pod" {
			all, match := true, false
			for _, cause := range errorDetails.Causes {
				if cause.Field == "spec" && strings.Contains(cause.Message, "may not update fields other than") {
					fmt.Fprintf(out, "error: may not update %s in pod %q directly\n", resource, errorDetails.Name)
					match = true
				} else {
					all = false
				}
			}
			if all && match {
				return
			}
		} else {
			if ok := kcmdutil.PrintErrorWithCauses(err, out); ok {
				return
			}
		}
	}

	fmt.Fprintf(out, "error: %v\n", err)
}

// selectString returns true if the provided string matches spec, where spec is a string with
// a non-greedy '*' wildcard operator.
// TODO: turn into a regex and handle greedy matches and backtracking.
func selectString(s, spec string) bool {
	if spec == "*" {
		return true
	}
	if !strings.Contains(spec, "*") {
		return s == spec
	}

	pos := 0
	match := true
	parts := strings.Split(spec, "*")
	for i, part := range parts {
		if len(part) == 0 {
			continue
		}
		next := strings.Index(s[pos:], part)
		switch {
		// next part not in string
		case next < pos:
			fallthrough
		// first part does not match start of string
		case i == 0 && pos != 0:
			fallthrough
		// last part does not exactly match remaining part of string
		case i == (len(parts)-1) && len(s) != (len(part)+next):
			match = false
			break
		default:
			pos = next
		}
	}
	return match
}

// Patch represents the result of a mutation to an object.
type Patch struct {
	Info *resource.Info
	Err  error

	Before []byte
	After  []byte
	Patch  []byte
}

// CalculatePatches calls the mutation function on each provided info object, and generates a strategic merge patch for
// the changes in the object. Encoder must be able to encode the info into the appropriate destination type. If mutateFn
// returns false, the object is not included in the final list of patches.
func CalculatePatches(infos []*resource.Info, encoder runtime.Encoder, mutateFn func(*resource.Info) (bool, error)) []*Patch {
	var patches []*Patch
	for _, info := range infos {
		patch := &Patch{Info: info}
		patch.Before, patch.Err = runtime.Encode(encoder, info.Object)
		if patch.Err != nil {
			patches = append(patches, patch)
			continue
		}

		ok, err := mutateFn(info)
		if err != nil {
			patch.Err = err
			patches = append(patches, patch)
			continue
		}
		if !ok {
			continue
		}
		patches = append(patches, patch)
		if patch.Err != nil {
			continue
		}

		patch.After, patch.Err = runtime.Encode(encoder, info.Object)
		if patch.Err != nil {
			continue
		}

		// TODO: should be via New
		versioned, err := info.Mapping.ConvertToVersion(info.Object, info.Mapping.GroupVersionKind.GroupVersion())
		if err != nil {
			patch.Err = err
			continue
		}

		patch.Patch, patch.Err = strategicpatch.CreateTwoWayMergePatch(patch.Before, patch.After, versioned)
	}
	return patches
}
