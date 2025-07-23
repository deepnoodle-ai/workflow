package activities

import (
	"crypto/rand"
	"fmt"
	mathrand "math/rand"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// RandomInput defines the input parameters for the random activity
type RandomInput struct {
	Type    string   `json:"type"`    // uuid, number, string, choice, boolean
	Min     float64  `json:"min"`     // minimum value for number generation
	Max     float64  `json:"max"`     // maximum value for number generation
	Length  int      `json:"length"`  // length for string generation
	Choices []string `json:"choices"` // choices for selection
	Count   int      `json:"count"`   // number of items to generate
	Charset string   `json:"charset"` // character set for string generation
	Seed    int64    `json:"seed"`    // seed for reproducible randomness
}

// RandomActivity can be used to generate random values
type RandomActivity struct{}

func NewRandomActivity() workflow.Activity {
	return workflow.NewTypedActivity(&RandomActivity{})
}

func (a *RandomActivity) Name() string {
	return "random"
}

func (a *RandomActivity) Execute(ctx workflow.Context, params RandomInput) (any, error) {
	if params.Type == "" {
		params.Type = "uuid"
	}

	// Initialize random generator
	var rng *mathrand.Rand
	if params.Seed != 0 {
		rng = mathrand.New(mathrand.NewSource(params.Seed))
	} else {
		rng = mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
	}

	// Set default count
	if params.Count <= 0 {
		params.Count = 1
	}

	var values []any

	for i := 0; i < params.Count; i++ {
		var value any
		var err error

		switch strings.ToLower(params.Type) {
		case "uuid", "guid":
			value, err = a.generateUUID()

		case "number", "int", "integer":
			if params.Max <= params.Min {
				params.Max = params.Min + 100 // default range
			}
			value = a.generateRandomNumber(rng, params.Min, params.Max)

		case "float", "decimal":
			if params.Max <= params.Min {
				params.Max = params.Min + 1.0 // default range
			}
			value = a.generateRandomFloat(rng, params.Min, params.Max)

		case "string", "text":
			length := params.Length
			if length <= 0 {
				length = 10 // default length
			}
			charset := params.Charset
			if charset == "" {
				charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
			}
			value = a.generateRandomString(rng, length, charset)

		case "choice", "select":
			if len(params.Choices) == 0 {
				err = fmt.Errorf("choices cannot be empty for choice type")
			} else {
				value = params.Choices[rng.Intn(len(params.Choices))]
			}

		case "boolean", "bool":
			value = rng.Intn(2) == 1

		case "alphanumeric":
			length := params.Length
			if length <= 0 {
				length = 8
			}
			value = a.generateRandomString(rng, length, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

		case "hex":
			length := params.Length
			if length <= 0 {
				length = 8
			}
			value = a.generateRandomString(rng, length, "0123456789abcdef")

		default:
			err = fmt.Errorf("unsupported type: %s", params.Type)
		}

		if err != nil {
			return nil, err
		}

		values = append(values, value)
	}

	if params.Count == 1 {
		return values[0], nil
	}
	return values, nil
}

// generateUUID generates a random UUID v4
func (a *RandomActivity) generateUUID() (string, error) {
	uuid := make([]byte, 16)
	if _, err := rand.Read(uuid); err != nil {
		return "", err
	}

	// Set version (4) and variant bits
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // Version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // Variant bits

	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:]), nil
}

// generateRandomNumber generates a random integer between min and max (inclusive)
func (a *RandomActivity) generateRandomNumber(rng *mathrand.Rand, min, max float64) int {
	minInt := int(min)
	maxInt := int(max)
	return rng.Intn(maxInt-minInt+1) + minInt
}

// generateRandomFloat generates a random float between min and max
func (a *RandomActivity) generateRandomFloat(rng *mathrand.Rand, min, max float64) float64 {
	return min + rng.Float64()*(max-min)
}

// generateRandomString generates a random string of specified length from charset
func (a *RandomActivity) generateRandomString(rng *mathrand.Rand, length int, charset string) string {
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[rng.Intn(len(charset))]
	}
	return string(result)
}
