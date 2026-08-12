//go:build !ck3_native || !cgo || !amd64

package indexer

import "image"

// buildMultiScaleRelief dispatches to the pure-Go implementation unless the
// ck3_native build tag selects the C implementation.
func buildMultiScaleRelief(heightmap image.Image) (*image.Gray, *image.Gray, *image.Gray) {
	return buildMultiScaleReliefGo(heightmap)
}
