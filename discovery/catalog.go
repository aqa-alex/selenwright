package discovery

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/aqa-alex/selenwright/config"
)

// AssembleCatalog builds a browser catalog from Docker image labels. The flow:
//
//  1. Scan all images with io.selenwright.browser labels.
//  2. Keep only images whose digest is in the adopted set.
//  3. If the active set (adopted ∩ present) is non-empty → build Config from labels.
//  4. If the active set is empty AND confPath is non-empty → fall back to JSON.
//  5. Return the populated Config (or empty if both sources yield nothing).
//
// cfg must be a pre-allocated *config.Config (e.g. from config.NewConfig()).
func AssembleCatalog(ctx context.Context, lister ImageLister, store *AdoptedStore, cfg *config.Config, confPath, logConfPath string) error {
	discovered, err := ScanImages(ctx, lister)
	if err != nil {
		return fmt.Errorf("scan images: %w", err)
	}

	browsers := buildBrowsersFromAdopted(discovered, store)

	if len(browsers) > 0 {
		cfg.Replace(browsers)
		log.Printf("[-] [DISCOVERY] [Built catalog from %d adopted image(s)]", countVersions(browsers))
		return nil
	}

	// Fallback to JSON when no adopted labels are present.
	if confPath != "" {
		log.Printf("[-] [DISCOVERY] [No adopted images found, falling back to %s]", confPath)
		return cfg.Load(confPath, logConfPath)
	}

	log.Printf("[-] [DISCOVERY] [No browsers discovered; no -conf fallback]")
	return nil
}

// buildBrowsersFromAdopted filters the discovered list to only adopted images
// and assembles the map[string]config.Versions structure.
func buildBrowsersFromAdopted(discovered []DiscoveredImage, store *AdoptedStore) map[string]config.Versions {
	browsers := make(map[string]config.Versions)

	for _, di := range discovered {
		if !store.IsAdopted(di.Digest) {
			continue
		}

		bv, ok := browsers[di.Name]
		if !ok {
			bv = config.Versions{Versions: make(map[string]*config.Browser)}
			browsers[di.Name] = bv
		}

		bv.Versions[di.Version] = di.Browser

		if di.Default {
			bv.Default = di.Version
			browsers[di.Name] = bv
		}
	}

	// For browsers with no explicit default, pick the highest version.
	for name, bv := range browsers {
		if bv.Default != "" {
			continue
		}
		bv.Default = highestVersion(bv.Versions)
		browsers[name] = bv
	}

	return browsers
}

// FilterUnadopted returns discovered images that are neither adopted nor dismissed.
func FilterUnadopted(discovered []DiscoveredImage, store *AdoptedStore) []DiscoveredImage {
	var out []DiscoveredImage
	for _, di := range discovered {
		if store.IsAdopted(di.Digest) || store.IsDismissed(di.Digest) {
			continue
		}
		out = append(out, di)
	}
	return out
}

// highestVersion returns the lexicographically largest version string that
// also satisfies basic semver ordering (dot-separated numeric segments).
func highestVersion(versions map[string]*config.Browser) string {
	keys := make([]string, 0, len(versions))
	for k := range versions {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return compareVersions(keys[i], keys[j]) < 0
	})
	if len(keys) == 0 {
		return ""
	}
	return keys[len(keys)-1]
}

// compareVersions does a best-effort semver-ish comparison.
// Falls back to string comparison for non-numeric segments.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		var na, nb int
		_, errA := fmt.Sscanf(pa[i], "%d", &na)
		_, errB := fmt.Sscanf(pb[i], "%d", &nb)
		if errA == nil && errB == nil {
			if na != nb {
				return na - nb
			}
			continue
		}
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return len(pa) - len(pb)
}

func countVersions(browsers map[string]config.Versions) int {
	n := 0
	for _, bv := range browsers {
		n += len(bv.Versions)
	}
	return n
}
