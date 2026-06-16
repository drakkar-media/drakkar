package archive

import (
	"testing"

	"github.com/hjongedijk/drakkar/internal/database"
)

func TestDetectImportedArchives(t *testing.T) {
	archives := DetectImportedArchives([]database.ImportedNZBFile{
		{FileName: "Movie.part01.rar"},
		{FileName: "Movie.part02.rar"},
		{FileName: "Movie.part03.rar"},
		{FileName: "Sample.mkv"},
	})
	if len(archives) != 1 {
		t.Fatalf("expected one archive group, got %+v", archives)
	}
	if archives[0].Kind != "rar" || archives[0].Status != "pending" {
		t.Fatalf("unexpected archive %+v", archives[0])
	}
	if len(archives[0].Volumes) != 3 {
		t.Fatalf("unexpected volumes %+v", archives[0].Volumes)
	}
	if archives[0].Volumes[0].VolumeIndex != 0 || archives[0].Volumes[2].VolumeIndex != 2 {
		t.Fatalf("unexpected volume ordering %+v", archives[0].Volumes)
	}
}

func TestDetectImportedArchivesWithRarAndR00(t *testing.T) {
	archives := DetectImportedArchives([]database.ImportedNZBFile{
		{FileName: "Release.rar"},
		{FileName: "Release.r00"},
		{FileName: "Release.r01"},
	})
	if len(archives) != 1 {
		t.Fatalf("expected one archive group, got %+v", archives)
	}
	if archives[0].Volumes[0].Path != "Release.rar" || archives[0].Volumes[1].Path != "Release.r00" {
		t.Fatalf("unexpected volumes %+v", archives[0].Volumes)
	}
}

func TestDetectImportedArchivesWith7z(t *testing.T) {
	archives := DetectImportedArchives([]database.ImportedNZBFile{
		{FileName: "Movie.7z.002"},
		{FileName: "Movie.7z.001"},
		{FileName: "Movie.7z.003"},
		{FileName: "Movie.mkv"},
	})
	if len(archives) != 1 {
		t.Fatalf("expected one archive group, got %+v", archives)
	}
	if archives[0].Kind != "7z" || archives[0].Status != "pending" {
		t.Fatalf("unexpected archive %+v", archives[0])
	}
	if len(archives[0].Volumes) != 3 {
		t.Fatalf("unexpected volumes %+v", archives[0].Volumes)
	}
	if archives[0].Volumes[0].Path != "Movie.7z.001" || archives[0].Volumes[2].VolumeIndex != 2 {
		t.Fatalf("unexpected volume ordering %+v", archives[0].Volumes)
	}
}
