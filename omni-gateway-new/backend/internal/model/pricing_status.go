package model

// PriceStatus uses explicit complete rate configuration, never rounded amounts.
// Unknown/missing/negative rates cannot be described as free.
func PriceStatus(p *PriceVersion) string {
	if p == nil || len(p.Cost) == 0 || len(p.Credits) == 0 {
		return "unpriced"
	}
	costZero, creditZero := true, true
	for meter, cost := range p.Cost {
		credit, ok := p.Credits[meter]
		if !ok || cost.IsNegative() || credit.IsNegative() {
			return "unpriced"
		}
		costZero = costZero && cost.IsZero()
	}
	for _, credit := range p.Credits {
		if credit.IsNegative() {
			return "unpriced"
		}
		creditZero = creditZero && credit.IsZero()
	}
	if costZero && creditZero {
		return "free"
	}
	if creditZero {
		return "credits_free"
	}
	return "priced"
}

// MergeSettlementStatus preserves error precedence across failover attempts.
func MergeSettlementStatus(current, next string) string {
	priority := map[string]int{"": 0, "none": 0, "free": 1, "credits_free": 2, "settled": 3, "unpriced": 4, "failed": 5}
	if priority[next] > priority[current] {
		return next
	}
	if current == "" {
		return "none"
	}
	return current
}
