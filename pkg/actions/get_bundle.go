// Copyright 2021 The Audit Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package actions

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os/exec"
	"path/filepath"
	"strings"

	apimanifests "github.com/operator-framework/api/pkg/manifests"
	"github.com/operator-framework/audit/pkg"
	"github.com/operator-framework/audit/pkg/models"
	log "github.com/sirupsen/logrus"
)

// Manifest define the manifest.json which is  required to read the bundle
type Manifest struct {
	Config string
	Layers []string
}

type DockerConfigManifest struct {
	DockerConfig DockerConfig `json:"Config"`
}

type DockerConfig struct {
	Labels map[string]string `json:"Labels"`
}

// GetDataFromBundleImage returns the bundle from the image
func GetDataFromBundleImage(auditBundle *models.AuditBundle,
	disableScorecard, disableValidators bool, label, labelValue string) *models.AuditBundle {

	downloadBundleImage(auditBundle)
	bundleDir := createBundleDir(auditBundle)
	dockerConfigManifest := extractBundleFromImage(auditBundle, bundleDir)

	if len(label) > 0 {
		value := dockerConfigManifest.DockerConfig.Labels[label]
		if value == labelValue {
			auditBundle.FoundLabel = true
		}
	}

	auditBundle.OCPLabel = dockerConfigManifest.DockerConfig.Labels["com.redhat.openshift.versions"]

	// Read the bundle
	var err error
	auditBundle.Bundle, err = apimanifests.GetBundleFromDir(filepath.Join(bundleDir, "bundle"))
	if err != nil {
		auditBundle.Errors = append(auditBundle.Errors, fmt.Errorf("unable to get the bundle: %s", err))
		return auditBundle
	}

	// Gathering data from scorecard
	if !disableScorecard {
		auditBundle.ScorecardResults, err = RunScorecard(filepath.Join(bundleDir, "bundle"))
		if err != nil {
			auditBundle.Errors = append(auditBundle.Errors, fmt.Errorf("unable to run scorecard: %s", err))
		}
	}

	if !disableValidators {
		auditBundle = RunValidators(auditBundle)
	}

	// Cleanup
	cleanupBundleDir(auditBundle, bundleDir)

	return auditBundle
}

func createBundleDir(auditBundle *models.AuditBundle) string {
	dir := fmt.Sprintf("./tmp/%s", auditBundle.OperatorBundleName)
	cmd := exec.Command("mkdir", dir)
	_, err := pkg.RunCommand(cmd)
	if err != nil {
		auditBundle.Errors = append(auditBundle.Errors,
			fmt.Errorf("unable to create the dir for the bundle: %s", err))
	}
	return dir
}

func downloadBundleImage(auditBundle *models.AuditBundle) {
	cmd := exec.Command("docker", "pull", auditBundle.OperatorBundleImagePath)
	_, err := pkg.RunCommand(cmd)
	if err != nil {
		auditBundle.Errors = append(auditBundle.Errors,
			fmt.Errorf("unable to create container image : %s", err))
	}
}

func extractBundleFromImage(auditBundle *models.AuditBundle, bundleDir string) DockerConfigManifest {
	imageName := strings.Split(auditBundle.OperatorBundleImagePath, "@")[0]
	tarPath := fmt.Sprintf("%s/%s.tar", bundleDir, auditBundle.OperatorBundleName)
	cmd := exec.Command("docker", "save", imageName, "-o", tarPath)
	_, err := pkg.RunCommand(cmd)
	if err != nil {
		log.Errorf("unable to save the bundle image : %s", err)
		auditBundle.Errors = append(auditBundle.Errors,
			fmt.Errorf("unable to save the bundle image : %s", err))
	}

	cmd = exec.Command("tar", "-xvf", tarPath, "-C", bundleDir)
	_, err = pkg.RunCommand(cmd)
	if err != nil {
		log.Errorf("unable to untar the bundle image: %s", err)
		auditBundle.Errors = append(auditBundle.Errors,
			fmt.Errorf("unable to untar the bundle image : %s", err))
	}

	cmd = exec.Command("mkdir", filepath.Join(bundleDir, "bundle"))
	_, err = pkg.RunCommand(cmd)
	if err != nil {
		log.Errorf("error to create the bundle bundleDir: %s", err)
		auditBundle.Errors = append(auditBundle.Errors,
			fmt.Errorf("error to create the bundle bundleDir : %s", err))
	}

	var dockerConfig DockerConfigManifest
	bundleConfigFilePath := filepath.Join(bundleDir, "manifest.json")
	existingFile, err := ioutil.ReadFile(bundleConfigFilePath)
	if err == nil {
		var bundleLayerConfig []Manifest
		if err := json.Unmarshal(existingFile, &bundleLayerConfig); err != nil {
			log.Errorf("unable to Unmarshal manifest.json: %s", err)
			auditBundle.Errors = append(auditBundle.Errors,
				fmt.Errorf("unable to Unmarshal manifest.json: %s", err))
		}
		if bundleLayerConfig == nil {
			log.Errorf("error to untar layers")
			auditBundle.Errors = append(auditBundle.Errors,
				fmt.Errorf("error to untar layers: %s", err))
		}

		bundleConfigFilePath := filepath.Join(bundleDir, bundleLayerConfig[0].Config)
		existingFile, err := ioutil.ReadFile(bundleConfigFilePath)
		if err == nil {
			if err := json.Unmarshal(existingFile, &dockerConfig); err != nil {
				log.Errorf("unable to Unmarshal manifest.json: %s", err)
				auditBundle.Errors = append(auditBundle.Errors,
					fmt.Errorf("unable to Unmarshal manifest.json: %s", err))
			}
		}

		for _, layer := range bundleLayerConfig[0].Layers {
			cmd = exec.Command("tar", "-xvf", filepath.Join(bundleDir, layer), "-C", filepath.Join(bundleDir, "bundle"))
			_, err = pkg.RunCommand(cmd)
			if err != nil {
				log.Errorf("unable to untar layer : %s", err)
				auditBundle.Errors = append(auditBundle.Errors,
					fmt.Errorf("error to untar layers : %s", err))
			}
		}
	} else {
		// If the docker manifest was not found then check if has just one layer
		cmd = exec.Command("tar", "-xvf", fmt.Sprintf("%s/layer.tar", bundleDir), "-C", filepath.Join(bundleDir, "bundle"))
		_, err = pkg.RunCommand(cmd)
		if err != nil {
			log.Errorf("unable to untar layer : %s", err)
			auditBundle.Errors = append(auditBundle.Errors,
				fmt.Errorf("unable to untar layer: %s", err))
		}
	}

	// Remove files in the image to allow load the bundle
	cmd = exec.Command("rm", "-rf", fmt.Sprintf("%s/bundle/manifests/.wh..wh..opq", bundleDir))
	_, _ = pkg.RunCommand(cmd)

	cmd = exec.Command("rm", "-rf", fmt.Sprintf("%s/bundle/metadata/.wh..wh..opq", bundleDir))
	_, _ = pkg.RunCommand(cmd)

	cmd = exec.Command("rm", "-rf", fmt.Sprintf("%s/bundle/root/", bundleDir))
	_, _ = pkg.RunCommand(cmd)

	return dockerConfig
}

func cleanupBundleDir(auditBundle *models.AuditBundle, dir string) {
	cmd := exec.Command("rm", "-rf", dir)
	_, _ = pkg.RunCommand(cmd)

	cmd = exec.Command("docker", "rmi", auditBundle.OperatorBundleImagePath)
	_, _ = pkg.RunCommand(cmd)
}