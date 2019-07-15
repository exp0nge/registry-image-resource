package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/sirupsen/logrus"

	gcr "github.com/simonshyu/notary-gcr/pkg/gcr"
	resource "github.com/concourse/registry-image-resource"
)

type OutRequest struct {
	Source resource.Source    `json:"source"`
	Params resource.PutParams `json:"params"`
}

type OutResponse struct {
	Version  resource.Version         `json:"version"`
	Metadata []resource.MetadataField `json:"metadata"`
}

func main() {
	logrus.SetOutput(os.Stderr)
	logrus.SetFormatter(&logrus.TextFormatter{
		ForceColors: true,
	})

	color.NoColor = false

	var req OutRequest
	decoder := json.NewDecoder(os.Stdin)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err != nil {
		logrus.Errorf("invalid payload: %s", err)
		os.Exit(1)
		return
	}

	if req.Source.Debug {
		logrus.SetLevel(logrus.DebugLevel)
	}

	if len(os.Args) < 2 {
		logrus.Errorf("destination path not specified")
		os.Exit(1)
		return
	}

	src := os.Args[1]

	ref, err := name.ParseReference(req.Source.Name(), name.WeakValidation)
	if err != nil {
		logrus.Errorf("could not resolve repository/tag reference: %s", err)
		os.Exit(1)
		return
	}

	tags, err := req.Params.ParseTags(src)
	if err != nil {
		logrus.Errorf("could not parse additional tags: %s", err)
		os.Exit(1)
		return
	}

	var extraRefs []name.Reference
	for _, tag := range tags {
		n := fmt.Sprintf("%s:%s", req.Source.Repository, tag)

		extraRef, err := name.ParseReference(n, name.WeakValidation)
		if err != nil {
			logrus.Errorf("could not resolve repository/tag reference: %s", err)
			os.Exit(1)
			return
		}

		extraRefs = append(extraRefs, extraRef)
	}

	imagePath := filepath.Join(src, req.Params.Image)

	img, err := tarball.ImageFromPath(imagePath, nil)
	if err != nil {
		logrus.Errorf("could not load image from path '%s': %s", req.Params.Image, err)
		os.Exit(1)
		return
	}

	digest, err := img.Digest()
	if err != nil {
		logrus.Errorf("failed to get image digest: %s", err)
		os.Exit(1)
		return
	}

	logrus.Infof("pushing %s to %s", digest, ref.Name())

	auth := &authn.Basic{
		Username: req.Source.Username,
		Password: req.Source.Password,
	}

	err = remote.Write(ref, img, auth, resource.RetryTransport)
	if err != nil {
		logrus.Errorf("failed to upload image: %s", err)
		os.Exit(1)
		return
	}

	logrus.Info("pushed")

	if req.Source.ContentTrust.Enable {
		notaryConfigDir, err := req.Source.ContentTrust.PrepareConfigDir(src)
		if err != nil {
			logrus.Errorf("failed to prepare notary-config-dir: %s", err)
			os.Exit(1)
			return
		}
		os.Setenv("NOTARY_CONFIG_DIR", notaryConfigDir)
		trustedRepo, err := gcr.NewTrustedGcrRepository(ref, auth)
		if err != nil {
			logrus.Errorf("failed to create TrustedGcrRepository: %s", err)
			os.Exit(1)
			return
		}
		err = trustedRepo.SignImage(img)
		if err != nil {
			logrus.Errorf("failed to sign image: %s", err)
		}
	}

	for _, extraRef := range extraRefs {
		logrus.Infof("tagging %s with %s", digest, extraRef.Identifier())

		err = remote.Write(extraRef, img, auth, http.DefaultTransport)
		if err != nil {
			logrus.Errorf("failed to tag image: %s", err)
			os.Exit(1)
			return
		}

		logrus.Info("tagged")
		if req.Source.ContentTrust.Enable {
			trustedRepo, err := gcr.NewTrustedGcrRepository(extraRef, auth)
			if err != nil {
				logrus.Errorf("failed to create TrustedGcrRepository: %s", err)
				os.Exit(1)
				return
			}
			err = trustedRepo.SignImage(img)
			if err != nil {
				logrus.Errorf("failed to sign image: %s", err)
			}
		}
	}

	json.NewEncoder(os.Stdout).Encode(OutResponse{
		Version: resource.Version{
			Digest: digest.String(),
		},
		Metadata: req.Source.MetadataWithAdditionalTags(tags),
	})
}
