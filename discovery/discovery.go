package discovery

import (
	"context"
	"fmt"
	"log"

	"github.com/aqa-alex/selenwright/config"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
)

// ImageLister abstracts docker client.Client.ImageList for testability.
type ImageLister interface {
	ImageList(ctx context.Context, options image.ListOptions) ([]image.Summary, error)
}

// DiscoveredImage is a browser image found on the Docker host via label
// introspection. It may or may not be adopted into the active catalog.
type DiscoveredImage struct {
	Digest   string          `json:"digest"`
	RepoTags []string        `json:"repoTags"`
	Browser  *config.Browser `json:"-"`
	Name     string          `json:"browser"`
	Version  string          `json:"version"`
	Protocol string          `json:"protocol"`
	Default  bool            `json:"isDefault"`
	Created  int64           `json:"-"`
}

// ScanImages queries the Docker daemon for images carrying the
// io.selenwright.browser label. Each qualifying image is parsed into a
// DiscoveredImage. When multiple images share the same browser+version,
// the one with the most recent Created timestamp wins.
func ScanImages(ctx context.Context, lister ImageLister) ([]DiscoveredImage, error) {
	opts := image.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", LabelBrowser)),
	}
	summaries, err := lister.ImageList(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("docker ImageList: %w", err)
	}

	type key struct{ name, version string }
	seen := make(map[key]int) // key → index in result slice
	var result []DiscoveredImage

	for _, img := range summaries {
		digest := imageDigest(img)
		if digest == "" {
			continue
		}

		browser, name, version, isDefault, err := ParseLabels(img.Labels)
		if err != nil {
			log.Printf("[-] [DISCOVERY] [Skipping image %s: %v]", digest, err)
			continue
		}

		// The Image field is the reference Docker will use to start the container.
		// Prefer the first repo tag; fall back to the digest.
		ref := digest
		if len(img.RepoTags) > 0 {
			ref = img.RepoTags[0]
		}
		browser.Image = ref

		di := DiscoveredImage{
			Digest:   digest,
			RepoTags: img.RepoTags,
			Browser:  browser,
			Name:     name,
			Version:  version,
			Protocol: browser.Protocol,
			Default:  isDefault,
			Created:  img.Created,
		}

		k := key{name, version}
		if idx, dup := seen[k]; dup {
			if img.Created > result[idx].Created {
				log.Printf("[-] [DISCOVERY] [Dedup %s %s: keeping %s over %s]", name, version, digest, result[idx].Digest)
				result[idx] = di
			} else {
				log.Printf("[-] [DISCOVERY] [Dedup %s %s: keeping %s over %s]", name, version, result[idx].Digest, digest)
			}
			continue
		}
		seen[k] = len(result)
		result = append(result, di)
	}

	return result, nil
}

// imageDigest returns the first repo digest or the image ID.
func imageDigest(img image.Summary) string {
	if len(img.RepoDigests) > 0 {
		return img.RepoDigests[0]
	}
	return img.ID
}
