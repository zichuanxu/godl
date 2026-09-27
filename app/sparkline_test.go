package app

import "testing"

func TestSparklineKeepsBoundedSamples(t *testing.T) {
	var sparkline Sparkline
	for i := 0; i < 80; i++ {
		sparkline.Add(int64(i))
	}
	if len(sparkline.Samples()) != 60 {
		t.Fatalf("sample count = %d; want 60", len(sparkline.Samples()))
	}
	if sparkline.Samples()[0] != 20 {
		t.Fatalf("oldest sample = %d; want 20", sparkline.Samples()[0])
	}
}
