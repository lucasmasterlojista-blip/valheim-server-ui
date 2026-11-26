package domain

// ProfilePackage identifies an exact Thunderstore package version selected
// for installation from a client profile comparison.
type ProfilePackage struct {
	Owner   string `json:"owner"`
	Name    string `json:"name"`
	Version string `json:"version"`
}
