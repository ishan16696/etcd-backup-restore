// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package snapstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	brtypes "github.com/gardener/etcd-backup-restore/pkg/types"
)

const (
	ocsCredentialDirectory string = "OPENSHIFT_APPLICATION_CREDENTIALS"      // #nosec G101 -- This is not a hardcoded password, but only a path to the credentials.
	ocsCredentialJSONFile  string = "OPENSHIFT_APPLICATION_CREDENTIALS_JSON" // #nosec G101 -- This is not a hardcoded password, but only a path to the credentials.
)

type ocsAuthOptions struct {
	Endpoint           string `json:"endpoint"`
	Region             string `json:"region"`
	AccessKeyID        string `json:"accessKeyID"`
	SecretAccessKey    string `json:"secretAccessKey"`
	DisableSSL         bool   `json:"disableSSL"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
}

// NewOCSSnapStore creates a new S3SnapStore from shared configuration with the specified bucket.
func NewOCSSnapStore(config *brtypes.SnapstoreConfig) (*S3SnapStore, error) {
	credentials, err := getOCSAuthOptions(getEnvPrefixString(config.IsSource))
	if err != nil {
		return nil, err
	}

	return newGenericS3FromAuthOpt(config.Container, config.Prefix, config.TempDir, config.MaxParallelChunkUploads, config.MinChunkSize, ocsAuthOptionsToGenericS3(*credentials))
}

func getOCSAuthOptions(prefix string) (*ocsAuthOptions, error) {
	if filename, isSet := os.LookupEnv(prefix + ocsCredentialJSONFile); isSet {
		ao, err := readOCSCredentialsJSON(filename)
		if err != nil {
			return nil, fmt.Errorf("error getting credentials using %v file with error: %w", filename, err)
		}
		return ao, nil
	}

	// TODO: @renormalize Remove this extra handling in v0.31.0
	// Check if a JSON file is present in the directory, if it is present -> the JSON file must be used for credentials.
	if dir, isSet := os.LookupEnv(prefix + ocsCredentialDirectory); isSet {
		jsonCredentialFile, err := findFileWithExtensionInDir(dir, ".json")
		if err != nil {
			return nil, fmt.Errorf("error while finding a JSON credential file in %v directory with error: %w", dir, err)
		}
		if jsonCredentialFile != "" {
			ao, err := readOCSCredentialsJSON(jsonCredentialFile)
			if err != nil {
				return nil, fmt.Errorf("error getting credentials using %v JSON file in a directory with error: %w", jsonCredentialFile, err)
			}
			return ao, nil
		}
		// Non JSON credential files might exist in the credential directory, do not return
	}

	if dir, isSet := os.LookupEnv(prefix + ocsCredentialDirectory); isSet {
		ao, err := readOCSCredentialFromDir(dir)
		if err != nil {
			return nil, fmt.Errorf("error getting credentials from %v directory with error: %w", dir, err)
		}
		return ao, nil
	}

	return nil, fmt.Errorf("unable to get credentials")
}

func readOCSCredentialFromDir(dirname string) (*ocsAuthOptions, error) {
	ao := ocsAuthOptions{}

	files, err := os.ReadDir(dirname)
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		switch file.Name() {
		case "endpoint":
			{
				data, err := os.ReadFile(dirname + "/endpoint") // #nosec G304 -- this is a trusted file, obtained via user input.
				if err != nil {
					return nil, err
				}
				ao.Endpoint = string(data)
			}
		case "accessKeyID":
			{
				data, err := os.ReadFile(dirname + "/accessKeyID") // #nosec G304 -- this is a trusted file, obtained via user input.
				if err != nil {
					return nil, err
				}
				ao.AccessKeyID = string(data)
			}
		case "secretAccessKey":
			{
				data, err := os.ReadFile(dirname + "/secretAccessKey") // #nosec G304 -- this is a trusted file, obtained via user input.
				if err != nil {
					return nil, err
				}
				ao.SecretAccessKey = string(data)
			}
		case "region":
			{
				data, err := os.ReadFile(dirname + "/region") // #nosec G304 -- this is a trusted file, obtained via user input.
				if err != nil {
					return nil, err
				}
				ao.Region = string(data)
			}
		case "disableSSL":
			{
				data, err := os.ReadFile(dirname + "/disableSSL") // #nosec G304 -- this is a trusted file, obtained via user input.
				if err != nil {
					return nil, err
				}
				ao.DisableSSL, err = strconv.ParseBool(string(data))
				if err != nil {
					return nil, err
				}
			}
		case "insecureSkipVerify":
			{
				data, err := os.ReadFile(dirname + "/insecureSkipVerify") // #nosec G304 -- this is a trusted file, obtained via user input.
				if err != nil {
					return nil, err
				}
				ao.InsecureSkipVerify, err = strconv.ParseBool(string(data))
				if err != nil {
					return nil, err
				}
			}
		}
	}

	if err := isOCSConfigEmpty(ao); err != nil {
		return nil, err
	}
	return &ao, nil
}

func readOCSCredentialsJSON(filename string) (*ocsAuthOptions, error) {
	jsonData, err := os.ReadFile(filename) // #nosec G304 -- this is a trusted file, obtained via user input.
	if err != nil {
		return nil, err
	}

	return ocsCredentialsFromJSON(jsonData)
}

// ocsCredentialsFromJSON obtains OCS credentials from a JSON value.
func ocsCredentialsFromJSON(jsonData []byte) (*ocsAuthOptions, error) {
	ocsConfig := ocsAuthOptions{}
	if err := json.Unmarshal(jsonData, &ocsConfig); err != nil {
		return nil, err
	}

	return &ocsConfig, nil
}

func ocsAuthOptionsToGenericS3(options ocsAuthOptions) s3AuthOptions {
	return s3AuthOptions{
		endpoint:           options.Endpoint,
		region:             options.Region,
		accessKeyID:        options.AccessKeyID,
		secretAccessKey:    options.SecretAccessKey,
		disableSSL:         options.DisableSSL,
		insecureSkipVerify: options.InsecureSkipVerify,
	}
}

func isOCSConfigEmpty(config ocsAuthOptions) error {
	if len(config.AccessKeyID) != 0 && len(config.Region) != 0 && len(config.SecretAccessKey) != 0 && len(config.Endpoint) != 0 {
		return nil
	}
	return fmt.Errorf("ocs s3 credentials: region, secretAccessKey, endpoint or accessKeyID is missing")
}

// GetOCSCredentialsLastModifiedTime returns the latest modification timestamp of the OCS credential file(s)
func GetOCSCredentialsLastModifiedTime() (time.Time, error) {
	// TODO: @renormalize Remove this extra handling in v0.31.0
	// Check if a JSON file is present in the directory, if it is present -> the JSON file must be used for credentials.
	if dir, isSet := os.LookupEnv(ocsCredentialDirectory); isSet {
		modificationTimeStamp, err := getJSONCredentialModifiedTime(dir)
		if err != nil {
			return time.Time{}, fmt.Errorf("failed to fetch credential modification time for OCS with error: %w", err)
		}
		if !modificationTimeStamp.IsZero() {
			return modificationTimeStamp, nil
		}
		// Non JSON credential files might exist in the credential directory, do not return
	}

	if dir, isSet := os.LookupEnv(ocsCredentialDirectory); isSet {
		// credential files which are essential for creating the snapstore
		credentialFiles := []string{"accessKeyID", "region", "endpoint", "secretAccessKey"}
		for i := range credentialFiles {
			credentialFiles[i] = filepath.Join(dir, credentialFiles[i])
		}
		ocsTimeStamp, err := getLatestCredentialsModifiedTime(credentialFiles)
		if err != nil {
			return time.Time{}, fmt.Errorf("failed to get OCS credential timestamp from the directory %v with error: %w", dir, err)
		}
		return ocsTimeStamp, nil
	}

	if filename, isSet := os.LookupEnv(ocsCredentialJSONFile); isSet {
		credentialFiles := []string{filename}
		ocsTimeStamp, err := getLatestCredentialsModifiedTime(credentialFiles)
		if err != nil {
			return time.Time{}, fmt.Errorf("failed to fetch file information of the OCS JSON credential file %v with error: %w", filename, err)
		}
		return ocsTimeStamp, nil
	}

	return time.Time{}, fmt.Errorf("no environment variable set for the OCS credential file")
}
