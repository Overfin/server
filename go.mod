module github.com/overfin/server

go 1.26.11

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/msfp-io/msfp-go v0.7.1
	github.com/overfin/web v0.99.21
	golang.org/x/crypto v0.59.0
	modernc.org/sqlite v1.62.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

// Until release day: msfp-go's revised v0 (branch revision) is not
// pushed, so the build reads the local checkout. On release day this
// line goes and the require above names the released version.
replace github.com/msfp-io/msfp-go => /home/c0pt3r/Projects/msfp-go

// Until release day: overfin-web's branch on the same model (its SDK this
// one's), from the local checkout too.
replace github.com/overfin/web => /home/c0pt3r/Projects/overfin-web
