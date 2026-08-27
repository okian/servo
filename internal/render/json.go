package render

import (
	"encoding/json"

	"github.com/okian/servo/v3/servo"
)

// JSON is the stable machine format — the same schema App.Graph()
// serializes to at runtime.
func JSON(g servo.Graph) (string, error) {
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}
