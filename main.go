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
	dlURL           = "https://golang.org/dl/?mode=json&include=all"
	fileDownloadFmt = "https://golang.org/dl/%s"
)

var staticBuffer = make([]byte, 1<<16)

func skipRelease(version string) bool {
	if strings.Contains(version, "beta") {
		return true
	}

	if strings.Contains(version, "rc") {
		return true
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
			if err = os.Mkdir(name, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "could not create %q: %v\n", name, err)
				return false
			}

			return true
		}

		fmt.Fprintf(os.Stderr, "could not stat %q: %v\n", name, err)
		return false
	}

	if info == nil {
		fmt.Fprintf(os.Stderr, "could not find %q: %v\n", name, err)
		return false
	}

	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "%q is not a directory\n", name)
		return false
	}

	return true
}

func dlTarget(dir string, file File) string { return path.Join(dir, file.Filename) }
func dlSHA(target string) string            { return target + ".sha" }

func checkHash(dir string, file File) bool {
	target := dlTarget(dir, file)
	info, err := os.Stat(target)
	if err != nil || info == nil {
		//fmt.Fprintf(os.Stderr, "could not stat %q: %v\n", target, err)
		return false
	}

	if info.Size() != int64(file.Size) {
		fmt.Fprintf(os.Stderr, "size of %q is %d, should be %d\n", target, info.Size(), file.Size)
		return false
	}

	hashFile := dlSHA(target)
	hd, err := ioutil.ReadFile(hashFile)
	if err != nil {
		if os.IsNotExist(err) {
			in, err := os.Open(target)
			if err != nil {
				fmt.Fprintf(os.Stderr, "could not read %q: %v\n", target, err)
				return false
			}

			defer in.Close()

			hasher := crypto.SHA256.New()
			if _, err = io.CopyBuffer(hasher, in, staticBuffer); err != nil {
				fmt.Fprintf(os.Stderr, "could not hash %q: %v\n", target, err)
				return false
			}

			hashBytes := hasher.Sum(nil)
			hash := hex.EncodeToString(hashBytes)

			if err = ioutil.WriteFile(hashFile, []byte(hash), 0644); err != nil {
				fmt.Fprintf(os.Stderr, "could not write hash to %q: %v\n", hashFile, err)
			}

			return file.SHA256Sum.Equal(hashBytes)
		}

		fmt.Fprintf(os.Stderr, "could not read %q: %v\n", hashFile, err)
		return false
	}

	hb, err := hex.DecodeString(string(hd))
	if err != nil {
		fmt.Fprintf(os.Stderr, "malformed hash file %q: %v\n", hashFile, err)
		return false
	}

	matches := file.SHA256Sum.Equal(hb)
	if !matches {
		fmt.Fprintf(os.Stderr, "%q sha does not match; expected %s, got %s\n", target, file.SHA256Sum, hd)
	}
	return matches
}

func main() {
	c := colly.NewCollector(
		colly.UserAgent("go-downloader v0.0.1 (johnweldon4@gmail.com)"),
	)

	c.SetRequestTimeout(30 * time.Second)

	c.OnResponse(func(r *colly.Response) {
		releases, err := Parse(bytes.NewReader(r.Body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing body: %v\n", err)
			return
		}

		for _, release := range releases {
			release_name := release.Version
			if skipRelease(release_name) {
				continue
			}

			if !ensureDirectory(release_name) {
				continue
			}

			for _, download := range release.Downloads {
				if skipFile(download) {
					continue
				}

				if checkHash(release_name, download) {
					fmt.Fprintf(os.Stdout, "%s/%s already downloaded\n", release_name, download.Filename)
					continue
				}

				fmt.Fprintf(os.Stdout, "%s/%s = %s\n", release_name, download.Filename, download.SHA256Sum)
				d := c.Clone()
				d.MaxBodySize = 1 << 29 // ~530MB
				d.SetRequestTimeout(10 * time.Minute)
				d.OnResponse(func(dr *colly.Response) {
					fmt.Fprintf(os.Stderr, "-- response %d\n", dr.StatusCode)
					if dr.StatusCode == http.StatusOK {
						target := dlTarget(release_name, download)
						if err = dr.Save(target); err != nil {
							fmt.Fprintf(os.Stderr, "could not save %q: %v\n", target, err)
							return
						}

						fmt.Fprintf(os.Stdout, "downloaded %q\n", target)

						hashFile := dlSHA(target)
						if err = ioutil.WriteFile(hashFile, []byte(download.SHA256Sum), 0644); err != nil {
							fmt.Fprintf(os.Stderr, "could not write hash file %q: %v\n", hashFile, err)
							return
						}

						fmt.Fprintf(os.Stdout, "saved hash %q\n", hashFile)
					}
				})

				dl := fmt.Sprintf(fileDownloadFmt, download.Filename)
				fmt.Fprintf(os.Stderr, "[%d] getting %s\n", d.ID, dl)
				if err = d.Visit(dl); err != nil {
					fmt.Fprintf(os.Stderr, "[%d] could not visit %q: %v\n", d.ID, dl, err)
				}
			}
		}
	})

	if err := c.Visit(dlURL); err != nil {
		fmt.Fprintf(os.Stderr, "Error visiting %s: %v\n", dlURL, err)
		os.Exit(-1)
	}
}

type Hash string

func (h Hash) Bytes() ([]byte, error) { return hex.DecodeString(string(h)) }
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
		return nil, err
	}

	return r, nil
}
