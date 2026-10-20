package theme

import "fmt"

// RGB is a colour channel triple, 0..255.
type RGB struct{ R, G, B int }

// Hex renders the colour as "#rrggbb".
func (c RGB) Hex() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }
