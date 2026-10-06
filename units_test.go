package whatsapp

import (
	"testing"
	"time"
)

func TestNormalizePhone(t *testing.T) {
	scenarios := []struct {
		raw      string
		expected string
	}{
		{"", ""},
		{"6165550100", ""},
		{"+0165550100", ""},
		{"+1234567", ""},
		{"+1234567890123456", ""},
		{"+1 616 abc", ""},
		{"+16165550100", "+16165550100"},
		{" +1 (616) 555-0100 ", "+16165550100"},
		{"+52.55.1234.5678", "+525512345678"},
		{"0044 20 7946 0000", "+442079460000"},
	}

	for _, s := range scenarios {
		result, err := NormalizePhone(s.raw)
		if s.expected == "" {
			if err == nil {
				t.Errorf("[%q] Expected error, got %q", s.raw, result)
			}
			continue
		}
		if err != nil || result != s.expected {
			t.Errorf("[%q] Expected %q, got %q (%v)", s.raw, s.expected, result, err)
		}
	}
}

func TestIsCountryAllowed(t *testing.T) {
	scenarios := []struct {
		phone    string
		allowed  []string
		expected bool
	}{
		{"+16165550100", nil, true},
		{"+16165550100", []string{"1"}, true},
		{"+16165550100", []string{"+1"}, true},
		{"+525512345678", []string{"1", "244"}, false},
		{"+244923000000", []string{"1", "244"}, true},
		{"+16165550100", []string{" "}, false},
	}

	for _, s := range scenarios {
		if r := isCountryAllowed(s.phone, s.allowed); r != s.expected {
			t.Errorf("[%s %v] Expected %v, got %v", s.phone, s.allowed, s.expected, r)
		}
	}
}

func TestPickLanguage(t *testing.T) {
	supported := []string{"en_US", "es_MX", "pt_BR"}

	scenarios := []struct {
		requested string
		supported []string
		expected  string
	}{
		{"", nil, "en_US"},
		{"fr", nil, "fr"},
		{"", supported, "en_US"},
		{"es_MX", supported, "es_MX"},
		{"es-mx", supported, "es_MX"},
		{"es_AR", supported, "es_MX"},
		{"pt", supported, "pt_BR"},
		{"fr_FR", supported, "en_US"},
	}

	for _, s := range scenarios {
		if r := pickLanguage(s.requested, s.supported, "en_US"); r != s.expected {
			t.Errorf("[%q %v] Expected %q, got %q", s.requested, s.supported, s.expected, r)
		}
	}
}

func TestWindowLimiter(t *testing.T) {
	now := time.Now()
	l := newWindowLimiter()
	l.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if ok, _ := l.allow("a", 2, time.Minute); !ok {
			t.Fatalf("[%d] Expected allowed", i)
		}
	}

	ok, retry := l.allow("a", 2, time.Minute)
	if ok || retry != time.Minute {
		t.Fatalf("Expected rejected with 1m retry, got %v %v", ok, retry)
	}

	// other keys are independent
	if ok, _ := l.allow("b", 2, time.Minute); !ok {
		t.Fatal("Expected b to be allowed")
	}

	now = now.Add(61 * time.Second)
	if ok, _ := l.allow("a", 2, time.Minute); !ok {
		t.Fatal("Expected allowed after the window")
	}

	now = now.Add(2 * time.Minute)
	l.prune(time.Minute)
	if len(l.hits) != 0 {
		t.Fatalf("Expected all hits to be pruned, got %v", l.hits)
	}
}
