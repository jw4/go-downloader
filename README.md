# go-downloader

Tool to download the Go(lang) releases.

This is helpful to avoid continually downloading the binaries repeatedly.

It's still in a very crude state (I probably won't do much to improve it, it does what I want).
Running this will check the Go download site and download all of the releases into folders in the current directory matching the version.
If the file is already downloaded it will check size and hash to see if it matches the official version and if so it will skip downloading that file again.
The tool will also write a companion file with a `.sha` suffix, containing the hash of the downloaded file, which it uses to check hashes in future runs.
