package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestGeo(t *testing.T) {
	if GeoFromPlace("  ") != nil || GeoFromPlace("нет") != nil || GeoFromPlace(nil) != nil {
		t.Fatal("пустое место должно давать nil")
	}
	if g := GeoFromPlace("«Токио, Япония»."); g == nil || g["name"] != "Токио, Япония" || g["source"] != "ai" {
		t.Fatalf("place: %v", g)
	}
	rec := analysisRecord(jx.M{"description": "d", "geo": GeoFromAnalysis(jx.M{"place": "Москва"})}, jx.M{"source": "Уфа Live"}, "t", "")
	if GeoClean(rec["geo"])["name"] != "Москва" {
		t.Fatalf("метка по содержанию: %v", rec["geo"])
	}
	rec = analysisRecord(jx.M{"description": "d"}, jx.M{"geo": nil}, "t", "")
	if _, ok := rec["geo"]; ok {
		t.Fatal("без места поля geo быть не должно")
	}
	if g := GeoFromAnalysis(jx.M{"place": "Москва", "place_lat": -37.0, "place_lon": 55.0}); g["lat"] != 55.75 {
		t.Fatalf("известное место берёт координаты из списка: %v", g)
	}
	if g := GeoFromAnalysis(jx.M{"place": "Мышкин", "place_lat": 57.79, "place_lon": 38.45}); g["lat"] != 57.79 || g["lon"] != 38.45 {
		t.Fatalf("правдоподобные числа модели остаются: %v", g)
	}
	if g := GeoFromAnalysis(jx.M{"place": "Мышкин", "place_lat": 957.0, "place_lon": 38.0}); g["lat"] != nil {
		t.Fatalf("невозможные координаты отбрасываются: %v", g)
	}
	if g := GeoFromAnalysis(jx.M{"place": "Мышкин"}); g == nil || g["lat"] != nil {
		t.Fatalf("без чисел — только название: %v", g)
	}
}
