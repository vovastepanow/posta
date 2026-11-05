package posta

import "testing"

func TestVKWebSearchCandidates(t *testing.T) {
	page := `<a href="https://vk.com/memes">one</a><a href="https://vk.com/memes">dup</a><a href="https://vk.com/club123">club</a><a href="https://vk.com/id123">profile</a><a href="https://evil.example/vk.com/bad">external</a><a href="https://vk.com/wall-2_123">post</a><a href="https://vk.com/memes/photos">subpage</a>`
	got := vkWebSearchCandidates(page)
	if len(got) != 2 || got[0].ID != "memes" || got[1].ID != "club123" {
		t.Fatalf("unexpected results: %v", got)
	}
}

func TestVKDirectoryCategories(t *testing.T) {
	page := `<a href="/vk/yumor/">Юмор и мемы<span>248</span></a><a href="/vk/memy/">Мемы<span>29</span></a><a href="https://evil.example/vk/memy/">Мемы</a><a href="/vk/eda/">Еда</a>`
	got := vkDirectoryCategories(page, []string{"мемы"})
	if len(got) != 2 || got[0] != "/vk/yumor/" || got[1] != "/vk/memy/" {
		t.Fatalf("categories: %v", got)
	}
}

func TestVKDirectoryResultsOnlyRows(t *testing.T) {
	page := `<a href="https://vk.com/unrelated">navigation</a><li class="row" data-k="vk:foo"><span class="r-u">vk.com/actual_memes</span></li><div>vk.com/recommendation</div>`
	got := vkDirectoryResults(page)
	if len(got) != 1 || got[0].ID != "actual_memes" {
		t.Fatalf("got %v", got)
	}
}
