// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

package doc_test

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/richardwilkes/pdfview/internal/doc"
)

// labelPDF builds a document with pageCount pages (objects 3 and up) whose catalog points /PageLabels at object 10.
// The number tree itself is supplied as tree, keyed by object number from 10 up, so a test can hand over anything from
// a single /Nums leaf to a hostile chain of /Kids. A nil tree leaves /PageLabels out of the catalog entirely, which is
// what an ordinary unlabeled document looks like.
func labelPDF(pageCount int, tree map[int]string) []byte {
	catalog := catalogObj
	if tree != nil {
		catalog = "<< /Type /Catalog /Pages 2 0 R /PageLabels 10 0 R >>"
	}
	objects := map[int]string{1: catalog}
	var kids strings.Builder
	for i := range pageCount {
		fmt.Fprintf(&kids, "%d 0 R ", 3+i)
		objects[3+i] = "<< /Type /Page /Parent 2 0 R >>"
	}
	objects[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), pageCount)
	maps.Copy(objects, tree)
	return pdf(objects)
}

// numsTree wraps a /Nums leaf array body as the whole (single-node) number tree.
func numsTree(nums string) map[int]string {
	return map[int]string{10: "<< /Nums [" + nums + "] >>"}
}

// checkLabels compares PageLabels against want and, when wantHas is meaningful to the case, HasPageLabels too.
func checkLabels(t *testing.T, d *doc.Document, want []string) {
	t.Helper()
	if got := d.PageLabels(); !slices.Equal(got, want) {
		t.Errorf("PageLabels() = %q, want %q", got, want)
	}
}

func checkHasLabels(t *testing.T, d *doc.Document, want bool) {
	t.Helper()
	if got := d.HasPageLabels(); got != want {
		t.Errorf("HasPageLabels() = %v, want %v", got, want)
	}
}

// TestPageLabelsBasic covers the shape nearly every real document uses — front matter in lowercase roman, the body in
// decimal — and the unlabeled document the decimal fallback has to cover indistinguishably, which is exactly why
// HasPageLabels exists to tell them apart.
func TestPageLabelsBasic(t *testing.T) {
	d := mustOpen(t, labelPDF(6, numsTree("0 << /S /r >> 4 << /S /D >>")))
	checkLabels(t, d, []string{"i", "ii", "iii", "iv", "1", "2"})
	checkHasLabels(t, d, true)

	plain := mustOpen(t, labelPDF(3, nil))
	checkLabels(t, plain, []string{"1", "2", "3"})
	checkHasLabels(t, plain, false)
}

// TestPageLabelsZeroPages guards the cache invariant against the one document that could break it: with no pages the
// built slice is empty, and an empty-but-non-nil slice is what keeps the build from re-running forever.
func TestPageLabelsZeroPages(t *testing.T) {
	d := mustOpen(t, labelPDF(0, numsTree("0 << /S /D >>")))
	if got := d.PageLabels(); got == nil || len(got) != 0 {
		t.Errorf("PageLabels() = %#v, want an empty, non-nil slice", got)
	}
	checkHasLabels(t, d, true)
}

// TestPageLabelStyles walks every style ISO 32000-2 12.4.2 defines plus the ways a range can combine them with /P and
// /St, including the alphabetic style's repetition wrap (Z then AA, not the base-26 "AA" that would follow Z if the
// letters counted like digits) and the empty range dictionary, whose empty labels are the spec's own answer.
func TestPageLabelStyles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		nums  string
		want  []string
		pages int
	}{
		{name: "decimal", nums: "0 << /S /D >>", pages: 3, want: []string{"1", "2", "3"}},
		{name: "upper roman", nums: "0 << /S /R >>", pages: 3, want: []string{"I", "II", "III"}},
		{name: "lower roman", nums: "0 << /S /r >>", pages: 3, want: []string{"i", "ii", "iii"}},
		{name: "upper alpha", nums: "0 << /S /A >>", pages: 3, want: []string{"A", "B", "C"}},
		{name: "lower alpha", nums: "0 << /S /a >>", pages: 3, want: []string{"a", "b", "c"}},
		{name: "start offset", nums: "0 << /S /D /St 5 >>", pages: 3, want: []string{"5", "6", "7"}},
		{name: "alpha wrap", nums: "0 << /S /A /St 26 >>", pages: 2, want: []string{"Z", "AA"}},
		{name: "prefix and style", nums: "0 << /S /D /P (A-) >>", pages: 2, want: []string{"A-1", "A-2"}},
		{name: "prefix only", nums: "0 << /P (Cover) >>", pages: 2, want: []string{"Cover", "Cover"}},
		{name: "empty range", nums: "0 << >>", pages: 2, want: []string{"", ""}},
		{name: "utf16 prefix", nums: "0 << /S /D /P <FEFF00C4002D> >>", pages: 1, want: []string{"Ä-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := mustOpen(t, labelPDF(tc.pages, numsTree(tc.nums)))
			checkLabels(t, d, tc.want)
			checkHasLabels(t, d, true)
		})
	}
}

// TestPageLabelFormattingEdges drives the number formatters to their boundaries through /St, which is the only way a
// document can reach them: the roman spot values, the caps past which each styled form gives up and prints decimal
// instead, the values roman and alpha have no representation for at all (zero and negatives), and the /St that would
// overflow int64 when the page offset is added if it were not clamped.
func TestPageLabelFormattingEdges(t *testing.T) {
	for _, tc := range []struct {
		name  string
		nums  string
		want  []string
		pages int
	}{
		{
			name: "roman spot values", nums: "0 << /S /r >>", pages: 8,
			want: []string{"i", "ii", "iii", "iv", "v", "vi", "vii", "viii"},
		},
		{name: "roman 1999", nums: "0 << /S /r /St 1999 >>", pages: 1, want: []string{"mcmxcix"}},
		{name: "roman 3999", nums: "0 << /S /R /St 3999 >>", pages: 1, want: []string{"MMMCMXCIX"}},
		{
			name: "roman at cap", nums: "0 << /S /r /St 65536 >>", pages: 1,
			want: []string{strings.Repeat("m", 65) + "dxxxvi"},
		},
		{name: "roman past cap", nums: "0 << /S /r /St 65537 >>", pages: 1, want: []string{"65537"}},
		{name: "roman zero", nums: "0 << /S /R /St 0 >>", pages: 1, want: []string{"0"}},
		{name: "roman negative", nums: "0 << /S /r /St -3 >>", pages: 1, want: []string{"-3"}},
		{name: "alpha at cap", nums: "0 << /S /a /St 1664 >>", pages: 1, want: []string{strings.Repeat("z", 64)}},
		{name: "alpha past cap", nums: "0 << /S /A /St 1665 >>", pages: 1, want: []string{"1665"}},
		{name: "alpha zero", nums: "0 << /S /a /St 0 >>", pages: 1, want: []string{"0"}},
		{name: "alpha negative", nums: "0 << /S /A /St -3 >>", pages: 1, want: []string{"-3"}},
		{name: "decimal negative", nums: "0 << /S /D /St -3 >>", pages: 2, want: []string{"-3", "-2"}},
		{
			// Unclamped, adding the page offset to this start would wrap into the negatives.
			name:  "start clamped high",
			nums:  "0 << /S /D /St " + strconv.FormatInt(math.MaxInt64, 10) + " >>",
			pages: 2,
			want:  []string{"4611686018427387904", "4611686018427387905"},
		},
		{
			name:  "start clamped low",
			nums:  "0 << /S /D /St " + strconv.FormatInt(math.MinInt64, 10) + " >>",
			pages: 2,
			want:  []string{"-4611686018427387904", "-4611686018427387903"},
		},
		{name: "real start truncates", nums: "0 << /S /D /St 7.9 >>", pages: 2, want: []string{"7", "8"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkLabels(t, mustOpen(t, labelPDF(tc.pages, numsTree(tc.nums))), tc.want)
		})
	}
}

// TestPageLabelsNoKeyZero: a tree whose first range starts partway into the document leaves the pages before it
// uncovered, and those fall back to their decimal ordinals rather than borrowing the first range's numbering.
func TestPageLabelsNoKeyZero(t *testing.T) {
	d := mustOpen(t, labelPDF(4, numsTree("2 << /S /r >>")))
	checkLabels(t, d, []string{"1", "2", "i", "ii"})
	checkHasLabels(t, d, true)
}

// TestPageLabelsOutOfOrderAndDuplicateKeys: /Nums is required to be sorted, so a file that writes it out of order gets
// sorted here rather than mis-assigned, and a key written twice keeps its first entry — the same first-wins rule the
// name-tree flattener applies.
func TestPageLabelsOutOfOrderAndDuplicateKeys(t *testing.T) {
	d := mustOpen(t, labelPDF(6, numsTree("4 << /S /D >> 0 << /S /r >> 0 << /S /A >>")))
	checkLabels(t, d, []string{"i", "ii", "iii", "iv", "1", "2"})
}

// TestPageLabelsKidsTreeIgnoresLimits splits the entries across /Kids and gives the second kid /Limits that exclude the
// key it actually holds. A limit-pruned search would never look inside it and would leave those pages on the first
// range; walking every kid finds the entry the file really wrote, which is the leniency indexNameTree already applies.
func TestPageLabelsKidsTreeIgnoresLimits(t *testing.T) {
	d := mustOpen(t, labelPDF(6, map[int]string{
		10: "<< /Kids [11 0 R 12 0 R] >>",
		11: "<< /Limits [0 3] /Nums [0 << /S /r >>] >>",
		12: "<< /Limits [90 99] /Nums [4 << /S /D >>] >>",
	}))
	checkLabels(t, d, []string{"i", "ii", "iii", "iv", "1", "2"})
	checkHasLabels(t, d, true)
}

// TestPageLabelsKidsCycle: a /Kids chain that points back at an ancestor must terminate on the visited set and still
// collect the entries it legitimately reached on the way down.
func TestPageLabelsKidsCycle(t *testing.T) {
	d := mustOpen(t, labelPDF(2, map[int]string{
		10: "<< /Kids [11 0 R] >>",
		11: "<< /Kids [10 0 R 12 0 R] >>", // Points back at its parent.
		12: "<< /Nums [0 << /S /r >>] >>",
	}))
	checkLabels(t, d, []string{"i", "ii"})
}

// TestPageLabelsDepthLimit: entries buried below the depth cap are simply not found, so their pages fall back to
// decimal ordinals and the document reports no labels at all — degrading, never erroring. The chain one level shallower
// is the control that shows it is the cap doing that and not the nesting itself. maxNumberTreeDepth is 64, and the
// chain's leaf sits at depth n, so n = 64 is the last one reached.
func TestPageLabelsDepthLimit(t *testing.T) {
	chain := func(n int) map[int]string {
		tree := make(map[int]string, n+1)
		for i := range n {
			tree[10+i] = fmt.Sprintf("<< /Kids [%d 0 R] >>", 11+i)
		}
		tree[10+n] = "<< /Nums [0 << /S /r >>] >>"
		return tree
	}
	within := mustOpen(t, labelPDF(2, chain(64)))
	checkLabels(t, within, []string{"i", "ii"})
	checkHasLabels(t, within, true)

	past := mustOpen(t, labelPDF(2, chain(65)))
	checkLabels(t, past, []string{"1", "2"})
	checkHasLabels(t, past, false)
}

// TestPageLabelsMalformed covers every way a number tree can be broken and pins the best-effort degradation each one
// gets. Nothing here errors and nothing here is dropped wholesale: a bad key costs its own pair, a bad value costs its
// own range, and an unusable tree costs only the labels it would have supplied.
func TestPageLabelsMalformed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tree    map[int]string
		want    []string
		pages   int
		wantHas bool
	}{
		{
			// The bad key costs its pair and nothing else: the step of two keeps the following key aligned.
			name: "non-integer key", tree: numsTree("(oops) << /S /A >> 2 << /S /r >>"), pages: 4,
			want: []string{"1", "2", "i", "ii"}, wantHas: true,
		},
		{
			name: "negative key", tree: numsTree("-1 << /S /r >>"), pages: 2,
			want: []string{"1", "2"}, wantHas: false,
		},
		{
			name: "key past maxPages", tree: numsTree("65536 << /S /r >>"), pages: 2,
			want: []string{"1", "2"}, wantHas: false,
		},
		{
			// A non-dictionary value is dropped rather than made into an empty range, so the previous range extends.
			name: "non-dict value", tree: numsTree("0 << /S /r >> 2 (junk)"), pages: 4,
			want: []string{"i", "ii", "iii", "iv"}, wantHas: true,
		},
		{
			name: "trailing key without value", tree: numsTree("0 << /S /r >> 4"), pages: 6,
			want: []string{"i", "ii", "iii", "iv", "v", "vi"}, wantHas: true,
		},
		{
			name: "unknown style", tree: numsTree("0 << /S /Q >>"), pages: 2,
			want: []string{"", ""}, wantHas: true,
		},
		{
			name: "non-string prefix", tree: numsTree("0 << /S /D /P 5 >>"), pages: 2,
			want: []string{"1", "2"}, wantHas: true,
		},
		{
			name: "non-name style", tree: numsTree("0 << /S (D) /P (p) >>"), pages: 2,
			want: []string{"p", "p"}, wantHas: true,
		},
		{
			name: "nums not an array", tree: map[int]string{10: "<< /Nums 5 >>"}, pages: 2,
			want: []string{"1", "2"}, wantHas: false,
		},
		{
			name: "tree not a dict", tree: map[int]string{10: "(not a tree)"}, pages: 2,
			want: []string{"1", "2"}, wantHas: false,
		},
		{
			name: "kid not a dict",
			tree: map[int]string{
				10: "<< /Kids [(junk) 11 0 R] >>",
				11: "<< /Nums [0 << /S /r >>] >>",
			},
			pages: 2, want: []string{"i", "ii"}, wantHas: true,
		},
		{
			name: "indirect key and value", tree: map[int]string{
				10: "<< /Nums [11 0 R 12 0 R] >>",
				11: "2",
				12: "<< /S /r >>",
			},
			pages: 4, want: []string{"1", "2", "i", "ii"}, wantHas: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := mustOpen(t, labelPDF(tc.pages, tc.tree))
			checkLabels(t, d, tc.want)
			checkHasLabels(t, d, tc.wantHas)
		})
	}
}

// encryptedPageLabelPDF builds a two-page document encrypted with the standard security handler at V1/R2, whose catalog
// and page tree sit directly in the file (readable before authentication, as in every real encrypted document) and
// whose /PageLabels tree carries an encrypted /P prefix. Labels built while the document is locked therefore hold raw
// ciphertext, which is what Authenticate has to invalidate.
func encryptedPageLabelPDF(t *testing.T, userPw, ownerPw string) []byte {
	t.Helper()
	id0 := []byte("0123456789abcdef")
	o := r2OwnerValue(t, ownerPw, userPw)
	fileKey := r2FileKey(userPw, o, allPermissions, id0)
	u := r2UserValue(t, fileKey)
	prefix := rc4Apply(t, r2ObjectKey(fileKey, 5, 0), []byte(encLabelPrefix))
	bodies := map[int]string{
		1: "<< /Type /Catalog /Pages 2 0 R /PageLabels 5 0 R >>",
		2: twoKidPages,
		3: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
		4: "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>",
		5: fmt.Sprintf("<< /Nums [0 << /S /D /P <%x> >>] >>", prefix),
		// The encryption dictionary's own strings are never encrypted (ISO 32000-2 7.6.2), so /O and /U go in as-is.
		6: fmt.Sprintf("<< /Filter /Standard /V 1 /R 2 /O <%x> /U <%x> /P -1 >>", o, u),
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(bodies)+1)
	for num := 1; num <= len(bodies); num++ {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, bodies[num])
	}
	xrefOff := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for num := 1; num < len(offsets); num++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[num])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Encrypt 6 0 R /ID [<%x> <%x>] >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets), id0, id0, xrefOff)
	return buf.Bytes()
}

// encLabelPrefix is the plaintext /P prefix inside encryptedPageLabelPDF.
const encLabelPrefix = "App-"

// TestPageLabelsRebuiltAfterAuthentication is the page-label half of the stale-cache problem
// TestNamedDestIndexRebuiltAfterAuthentication pins for named destinations. Nothing gates page labels on a password, so
// a query made while the document is locked caches labels whose /P prefix is the ciphertext DecryptString passed
// through untouched — and the page count the slice is sized to can change under it too. Authenticate must drop the
// cache alongside the COS object caches.
func TestPageLabelsRebuiltAfterAuthentication(t *testing.T) {
	for _, password := range []string{pwUser, pwOwner} {
		t.Run(password, func(t *testing.T) {
			d, err := doc.Open(encryptedPageLabelPDF(t, pwUser, pwOwner))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !d.NeedsPassword() {
				t.Fatal("NeedsPassword() = false, want true")
			}
			want := []string{encLabelPrefix + "1", encLabelPrefix + "2"}
			// The page tree is never encrypted, so this query happens on the locked document and caches the poisoned
			// labels. It must not already read as plaintext, or the test is not building the cache it means to.
			if locked := d.PageLabels(); slices.Equal(locked, want) {
				t.Fatalf("PageLabels() = %q on a locked document; the prefix was not encrypted", locked)
			}
			if status := d.Authenticate(password); status == 0 {
				t.Fatalf("Authenticate(%q) failed", password)
			}
			checkLabels(t, d, want)
			checkHasLabels(t, d, true)
		})
	}
}

// TestPageLabelsSurviveFailedAuthentication guards the other side: a wrong password changes nothing, so the labels
// built while locked are still the right ones for the still-locked document.
func TestPageLabelsSurviveFailedAuthentication(t *testing.T) {
	d, err := doc.Open(encryptedPageLabelPDF(t, pwUser, pwOwner))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	locked := slices.Clone(d.PageLabels())
	if len(locked) != 2 {
		t.Fatalf("PageLabels() = %q before authentication, want 2 entries", locked)
	}
	if status := d.Authenticate("wrong"); status != 0 {
		t.Fatalf("Authenticate(\"wrong\") = %d, want 0", status)
	}
	checkLabels(t, d, locked)
}
