package main

import (
	"bytes"
	"crypto"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/gocolly/colly"
)

const (
	userAgent          = "go-downloader v0.0.1 (johnweldon4@gmail.com)"
	dlURL              = "https://golang.org/dl/?mode=json&include=all"
	fileDownloadFmt    = "https://golang.org/dl/%s"
	bufferSize         = 1 << 20 // ~1MB
	maxDownloadBody    = 1 << 29 // ~530MB
	maxDownloadTimeout = 10 * time.Minute
	requestTimeout     = 30 * time.Second
	dirPerms           = 0o755
	filePerms          = 0o644
)

// nolint: gochecknoglobals
var (
	skipVersions = []string{"go1.2"}
	skipWords    = []string{"beta", "rc"}
	staticBuffer = make([]byte, bufferSize)
	errOut       io.Writer
	statusOut    io.Writer
)

func main() {
	statusOut = os.Stdout
	errOut = os.Stderr

	c := colly.NewCollector(colly.UserAgent(userAgent))
	c.SetRequestTimeout(requestTimeout)

	c.OnResponse(func(r *colly.Response) {
		releases, err := Parse(bytes.NewReader(r.Body))
		if err != nil {
			fmt.Fprintf(errOut, "error parsing releases: %v\n", err)

			return
		}

		for _, release := range releases {
			releaseName := release.Version
			if skipRelease(releaseName) {
				fmt.Fprintf(errOut, "skipping %s\n", releaseName)

				continue
			}

			if !ensureDirectory(releaseName) {
				continue
			}

			for _, download := range release.Downloads {
				if skipFile(download) {
					fmt.Fprintf(errOut, "skipping %s/%s\n", releaseName, download.Filename)

					continue
				}

				if checkHash(releaseName, download) {
					fmt.Fprintf(errOut, "already downloaded %s/%s\n", releaseName, download.Filename)

					continue
				}

				fmt.Fprintf(statusOut, "downloading %s/%s [%s]\n", releaseName, download.Filename, download.SHA256Sum)
				downloadFile(c, releaseName, download)
			}
		}
	})

	if err := c.Visit(dlURL); err != nil {
		fmt.Fprintf(errOut, "error visiting %s: %v\n", dlURL, err)
		os.Exit(-1)
	}
}

func skipRelease(version string) bool {
	for _, ver := range skipVersions {
		if ver == version {
			return true
		}
	}

	for _, word := range skipWords {
		if strings.Contains(version, word) {
			return true
		}
	}

	return false
}

func skipFile(file File) bool {
	if file.Size == 0 {
		return true
	}

	if len(file.SHA256Sum) == 0 {
		return true
	}

	return false
}

func ensureDirectory(name string) bool {
	info, err := os.Stat(name)
	if err != nil {
		if os.IsNotExist(err) {
			if err = os.Mkdir(name, dirPerms); err != nil {
				fmt.Fprintf(errOut, "could not create %q: %v\n", name, err)

				return false
			}

			return true
		}

		fmt.Fprintf(errOut, "could not stat %q: %v\n", name, err)

		return false
	}

	if info == nil {
		fmt.Fprintf(errOut, "could not find %q: %v\n", name, err)

		return false
	}

	if !info.IsDir() {
		fmt.Fprintf(errOut, "%q is not a directory\n", name)

		return false
	}

	return true
}

func dlTarget(dir string, file File) string { return path.Join(dir, file.Filename) }
func dlSHA(target string) string            { return target + ".sha" }

func computeHash(target string) []byte {
	in, err := os.Open(target)
	if err != nil {
		fmt.Fprintf(errOut, "could not read %q: %v\n", target, err)

		return nil
	}

	defer in.Close()

	hasher := crypto.SHA256.New()
	if _, err = io.CopyBuffer(hasher, in, staticBuffer); err != nil {
		fmt.Fprintf(errOut, "could not hash %q: %v\n", target, err)

		return nil
	}

	hashBytes := hasher.Sum(nil)
	hash := hex.EncodeToString(hashBytes)

	hashFile := dlSHA(target)
	_ = writeHash(hashFile, hash)

	return hashBytes
}

func checkHash(dir string, file File) bool {
	target := dlTarget(dir, file)

	info, err := os.Stat(target)
	if err != nil || info == nil {
		return false
	}

	if info.Size() != int64(file.Size) {
		fmt.Fprintf(errOut, "size of %q is %d, should be %d\n", target, info.Size(), file.Size)

		return false
	}

	hashFile := dlSHA(target)

	hd, err := ioutil.ReadFile(hashFile)
	if err != nil {
		if os.IsNotExist(err) {
			hashBytes := computeHash(target)

			return file.SHA256Sum.Equal(hashBytes)
		}

		fmt.Fprintf(errOut, "could not read %q: %v\n", hashFile, err)

		return false
	}

	hb, err := hex.DecodeString(string(hd))
	if err != nil {
		fmt.Fprintf(errOut, "malformed hash file %q: %v\n", hashFile, err)

		return false
	}

	matches := file.SHA256Sum.Equal(hb)
	if !matches {
		fmt.Fprintf(errOut, "%q sha does not match; expected %s, got %s\n", target, file.SHA256Sum, hd)
	}

	return matches
}

func downloadFile(c *colly.Collector, releaseName string, download File) {
	d := c.Clone()
	d.MaxBodySize = maxDownloadBody
	d.SetRequestTimeout(maxDownloadTimeout)

	d.OnResponse(func(dr *colly.Response) {
		if dr.StatusCode == http.StatusOK {
			target := dlTarget(releaseName, download)
			if err := dr.Save(target); err != nil {
				fmt.Fprintf(errOut, "  [%d] could not save %q: %v\n", d.ID, target, err)

				return
			}

			fmt.Fprintf(statusOut, "  [%d] downloaded %q\n", d.ID, target)

			hashFile := dlSHA(target)

			if err := writeHash(hashFile, string(download.SHA256Sum)); err != nil {
				return
			}

			fmt.Fprintf(statusOut, "  [%d] saved hash %q\n", d.ID, hashFile)
		}
	})

	dl := fmt.Sprintf(fileDownloadFmt, download.Filename)
	fmt.Fprintf(errOut, "  [%d] getting %s\n", d.ID, dl)

	if err := d.Visit(dl); err != nil {
		fmt.Fprintf(errOut, "  [%d] could not visit %q: %v\n", d.ID, dl, err)
	}
}

func writeHash(hashFile, hash string) error {
	if err := ioutil.WriteFile(hashFile, []byte(hash), filePerms); err != nil {
		fmt.Fprintf(errOut, "could not write hash to %q: %v\n", hashFile, err)

		return err // nolint: wrapcheck
	}

	return nil
}

type Hash string

func (h Hash) Bytes() ([]byte, error) { return hex.DecodeString(string(h)) } // nolint: wrapcheck
func (h Hash) Equal(other []byte) bool {
	self, err := h.Bytes()
	if err != nil {
		return false
	}

	if len(self) != len(other) {
		return false
	}

	for ix := range self {
		if self[ix] != other[ix] {
			return false
		}
	}

	return true
}

type File struct {
	Filename     string `json:"filename"`
	OS           string `json:"os"`
	Architecture string `json:"arch"`
	Version      string `json:"version"`
	SHA256Sum    Hash   `json:"sha256"`
	Size         uint64 `json:"size"`
	Kind         string `json:"kind"`
}

type Release struct {
	Version   string `json:"version"`
	IsStable  bool   `json:"stable"`
	Downloads []File `json:"files"`
}

type Releases []Release

func Parse(data io.Reader) (Releases, error) {
	var r Releases
	if err := json.NewDecoder(data).Decode(&r); err != nil {
		return nil, err // nolint: wrapcheck
	}

	return r, nil
}
