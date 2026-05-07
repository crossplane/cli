/*
Copyright 2026 The Crossplane Authors.

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

package project

import (
	"slices"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
)

// AnnotateImage reads in the layers of the given v1.Image and annotates the
// xpkg layers with their corresponding annotations, returning a new v1.Image
// containing the annotation details.
func AnnotateImage(i v1.Image) (v1.Image, error) {
	cfgFile, err := i.ConfigFile()
	if err != nil {
		return nil, err
	}

	layers, err := i.Layers()
	if err != nil {
		return nil, err
	}

	addendums := make([]mutate.Addendum, 0)

	for _, l := range layers {
		d, err := l.Digest()
		if err != nil {
			return nil, err
		}
		if annotation, ok := cfgFile.Config.Labels[xpkg.Label(d.String())]; ok {
			addendums = append(addendums, mutate.Addendum{
				Layer:     l,
				MediaType: types.DockerLayer,
				Annotations: map[string]string{
					xpkg.AnnotationKey: annotation,
				},
			})
			continue
		}
		addendums = append(addendums, mutate.Addendum{
			Layer:     l,
			MediaType: types.DockerLayer,
		})
	}

	if len(addendums) == 0 {
		return i, nil
	}

	img := empty.Image
	for _, a := range addendums {
		img, err = mutate.Append(img, a)
		if err != nil {
			return nil, errors.Wrap(err, "failed to build annotated image")
		}
	}

	img, err = mutate.ConfigFile(img, cfgFile)
	if err != nil {
		return nil, err
	}

	img = mutate.MediaType(img, types.DockerManifestSchema2)
	img = mutate.ConfigMediaType(img, types.DockerConfigJSON)

	return img, nil
}

// BuildIndex applies annotations to each of the given images and then generates
// an index for them. The annotated images are returned so that a caller can
// push them before pushing the index, since the passed images may not match the
// annotated images.
func BuildIndex(imgs ...v1.Image) (v1.ImageIndex, []v1.Image, error) {
	adds := make([]mutate.IndexAddendum, 0, len(imgs))
	images := make([]v1.Image, 0, len(imgs))
	for _, img := range imgs {
		aimg, err := AnnotateImage(img)
		if err != nil {
			return nil, nil, err
		}
		images = append(images, aimg)
		mt, err := aimg.MediaType()
		if err != nil {
			return nil, nil, err
		}

		conf, err := aimg.ConfigFile()
		if err != nil {
			return nil, nil, err
		}

		adds = append(adds, mutate.IndexAddendum{
			Add: aimg,
			Descriptor: v1.Descriptor{
				MediaType: mt,
				Platform: &v1.Platform{
					Architecture: conf.Architecture,
					OS:           conf.OS,
					OSVersion:    conf.OSVersion,
				},
			},
		})
	}

	var sortErr error
	slices.SortFunc(adds, func(a, b mutate.IndexAddendum) int {
		dgstA, errA := a.Add.Digest()
		dgstB, errB := b.Add.Digest()
		sortErr = errors.Join(errA, errB)
		return strings.Compare(dgstA.String(), dgstB.String())
	})
	if sortErr != nil {
		return nil, nil, sortErr
	}

	return mutate.AppendManifests(empty.Index, adds...), images, nil
}
