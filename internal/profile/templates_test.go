package profile

import "testing"

// The category templates in templates/ are shipped scaffolds. This guards them
// against drift: every one must parse and validate as the profile format
// evolves, and collectively they must exercise each category.
func TestTemplatesValidate(t *testing.T) {
	profiles, err := Load("../../templates")
	if err != nil {
		t.Fatalf("templates should all load and validate: %v", err)
	}

	wantCategories := []Category{
		CategoryMeter, CategoryChargeController, CategoryShunt,
		CategoryInverter, CategoryBMS,
	}
	seen := make(map[Category]bool)
	for _, p := range profiles {
		seen[p.Category] = true
	}
	for _, c := range wantCategories {
		if !seen[c] {
			t.Errorf("no template found for category %q", c)
		}
	}
}
