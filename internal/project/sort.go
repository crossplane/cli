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
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// SortImages analyzes an image map produced by the project builder and picks
// out the configuration and function images from it. The function images are
// grouped together by function, so that multi-arch indexes can be produced
// based on the returned map.
func SortImages(imgMap ImageTagMap, repo string) (cfgImage v1.Image, fnImages map[name.Repository][]v1.Image, err error) {
	cfgTag, err := name.NewTag(fmt.Sprintf("%s:%s", repo, ConfigurationTag), name.StrictValidation)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to construct configuration tag")
	}

	fnImages = make(map[name.Repository][]v1.Image)
	for tag, image := range imgMap {
		if tag == cfgTag {
			cfgImage = image
			continue
		}

		fnImages[tag.Repository] = append(fnImages[tag.Repository], image)
	}

	if cfgImage == nil {
		return nil, nil, errors.New("failed to find configuration image")
	}

	return cfgImage, fnImages, nil
}
