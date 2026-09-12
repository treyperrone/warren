package browser

import "testing"

// Two destinations differing only in case or punctuation (an instance named "Web-01" vs
// "web 01", say) slug to the same nickname — harmless for the picker, which matches by full
// destination, but ambiguous for `warren exec/shell <nickname>`, which matches by nickname
// alone. UniqueNickname is what disambiguates them.
func TestUniqueNicknameDisambiguatesADifferentDestination(t *testing.T) {
	isolateHome(t)
	first := Favorite{Nickname: "web-01-rdp", StartURL: "https://ex.awsapps.com/start", AccountID: "1", Role: "R", InstanceName: "Web-01", ConnType: "rdp"}
	if err := AddFavorite(first); err != nil {
		t.Fatal(err)
	}

	other := Favorite{StartURL: "https://ex.awsapps.com/start", AccountID: "1", Role: "R", InstanceName: "web 01", ConnType: "rdp"}
	got := UniqueNickname("web-01-rdp", other)

	if got != "web-01-rdp-2" {
		t.Errorf("UniqueNickname = %q, want a disambiguated %q", got, "web-01-rdp-2")
	}
}

// Re-favoriting the SAME destination must keep the same nickname — AddFavorite's own
// Same(dest) check updates that entry in place, and disambiguating here would defeat that,
// piling up a new favorite instead of replacing the existing one.
func TestUniqueNicknameKeepsItForTheSameDestination(t *testing.T) {
	isolateHome(t)
	fav := Favorite{Nickname: "web-01-rdp", StartURL: "https://ex.awsapps.com/start", AccountID: "1", Role: "R", InstanceName: "Web-01", ConnType: "rdp"}
	if err := AddFavorite(fav); err != nil {
		t.Fatal(err)
	}

	got := UniqueNickname("web-01-rdp", fav)

	if got != "web-01-rdp" {
		t.Errorf("UniqueNickname = %q, want the same nickname unchanged", got)
	}
}

// No collision at all: the base name passes through untouched.
func TestUniqueNicknameNoCollision(t *testing.T) {
	isolateHome(t)
	dest := Favorite{StartURL: "https://ex.awsapps.com/start", AccountID: "1", Role: "R"}
	if got := UniqueNickname("prod-admin", dest); got != "prod-admin" {
		t.Errorf("UniqueNickname = %q, want %q unchanged", got, "prod-admin")
	}
}

// Three or more distinct destinations sharing a base name each get their own suffix.
func TestUniqueNicknameHandlesMultipleCollisions(t *testing.T) {
	isolateHome(t)
	base := Favorite{StartURL: "https://ex.awsapps.com/start", AccountID: "1", Role: "R", ConnType: "rdp"}
	a := base
	a.InstanceName = "a"
	a.Nickname = "web-01-rdp"
	if err := AddFavorite(a); err != nil {
		t.Fatal(err)
	}

	b := base
	b.InstanceName = "b"
	b.Nickname = UniqueNickname("web-01-rdp", b)
	if err := AddFavorite(b); err != nil {
		t.Fatal(err)
	}

	c := base
	c.InstanceName = "c"
	got := UniqueNickname("web-01-rdp", c)

	if got != "web-01-rdp-3" {
		t.Errorf("UniqueNickname = %q, want %q — the first two suffixes are already taken", got, "web-01-rdp-3")
	}
}
