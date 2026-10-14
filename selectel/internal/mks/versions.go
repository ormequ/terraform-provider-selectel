package mks

import (
	"crypto/md5" // #nosec G501
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// The helpers below repeat the ones of the _v1 data sources in package
// selectel, which this package cannot import, so that _v1 and _v2 return the
// same IDs and versions.

// checksum is the ID of a list: the MD5 of its concatenated items.
func checksum(items ...string) string {
	// #nosec G401
	sum := md5.Sum([]byte(strings.Join(items, ""))) // nosemgrep: go.lang.security.audit.crypto.use_of_weak_crypto.use-of-md5

	return fmt.Sprintf("%x", sum)
}

// sortedChecksum is checksum over a sorted copy of items.
func sortedChecksum(items []string) string {
	return checksum(slices.Sorted(slices.Values(items))...)
}

// minorVersion trims a Kubernetes version such as v1.31.2 to 1.31.
func minorVersion(kubeVersion string) (string, error) {
	major, err := majorPart(kubeVersion)
	if err != nil {
		return "", err
	}
	minor, err := minorPart(kubeVersion)
	if err != nil {
		return "", err
	}

	return strconv.Itoa(major) + "." + strconv.Itoa(minor), nil
}

// latestVersion returns the version with the highest minor part; of versions
// with the same minor part the first one wins.
func latestVersion(versions []string) (string, error) {
	var latest string
	latestMinor := -1
	for _, version := range versions {
		minor, err := minorPart(version)
		if err != nil {
			return "", fmt.Errorf("unable to compare minor parts of kube versions: %w", err)
		}
		if minor > latestMinor {
			latest, latestMinor = version, minor
		}
	}

	return latest, nil
}

func majorPart(kubeVersion string) (int, error) {
	kubeVersion = strings.TrimPrefix(kubeVersion, "v")
	major, err := strconv.Atoi(strings.Split(kubeVersion, ".")[0])
	if err != nil {
		return 0, invalidVersion(kubeVersion, "major part is not an integer number")
	}
	if major < 0 {
		return 0, invalidVersion(kubeVersion, "major part is a negative number")
	}

	return major, nil
}

func minorPart(kubeVersion string) (int, error) {
	kubeVersion = strings.TrimPrefix(kubeVersion, "v")
	parts := strings.Split(kubeVersion, ".")
	if len(parts) < 2 {
		return 0, invalidVersion(kubeVersion, "expected to have major and minor version parts")
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, invalidVersion(kubeVersion, "minor part is not an integer number")
	}
	if minor < 0 {
		return 0, invalidVersion(kubeVersion, "minor part is a negative number")
	}

	return minor, nil
}

func invalidVersion(kubeVersion, reason string) error {
	return fmt.Errorf("kube version %s is invalid: %s", kubeVersion, reason)
}
