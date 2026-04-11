// Struct environment: fields and methods on the env value are both
// reachable as identifiers. Methods with no arguments can also be
// called positionally.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/deepnoodle-ai/expr"
)

type Order struct {
	ID       string
	Customer string
	Total    float64
	Items    []LineItem
	Country  string
}

type LineItem struct {
	SKU      string
	Quantity int
	Price    float64
}

// Subtotal is a bound method — callable from an expression as
// `Subtotal()`. The receiver is the env value.
func (o Order) Subtotal() float64 {
	var total float64
	for _, it := range o.Items {
		total += it.Price * float64(it.Quantity)
	}
	return total
}

// ShipsInternational is another method; expr treats no-argument
// bound methods as zero-arg callables.
func (o Order) ShipsInternational() bool {
	return strings.ToUpper(o.Country) != "US"
}

func main() {
	ctx := context.Background()

	order := Order{
		ID:       "A-1042",
		Customer: "Ada Lovelace",
		Total:    129.50,
		Country:  "GB",
		Items: []LineItem{
			{SKU: "widget", Quantity: 2, Price: 19.99},
			{SKU: "gadget", Quantity: 1, Price: 89.52},
		},
	}

	exprs := []string{
		// direct field access
		`ID`,
		`Customer`,
		`Country`,

		// indexing into a slice field
		`Items[0].SKU`,
		`Items[1].Price`,
		`len(Items)`,

		// bound methods called with no args
		`Subtotal()`,
		`ShipsInternational()`,

		// combine method results with arithmetic
		`Subtotal() > 100`,

		// predicates across both fields and methods
		`Customer == "Ada Lovelace" && ShipsInternational()`,
	}

	fmt.Printf("order: %s for %s (%s)\n\n", order.ID, order.Customer, order.Country)
	fmt.Println("struct-env expressions:")
	for _, code := range exprs {
		p, err := expr.Compile(code, expr.WithBuiltins())
		if err != nil {
			fmt.Printf("  %-40s  ERROR: %v\n", code, err)
			continue
		}
		v, err := p.Run(ctx, order)
		if err != nil {
			fmt.Printf("  %-40s  ERROR: %v\n", code, err)
			continue
		}
		fmt.Printf("  %-40s => %v\n", code, v)
	}
}
