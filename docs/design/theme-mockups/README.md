# TollGate admin/balance theme — mockups and the contrast audit

For **t_a7d821a7**. The report was that the admin page is still on the net4sats theme and
that **"the white colour is hard to read on a glossy screen"**.

## The headline finding: the button label fails WCAG, and it does so TODAY

The complaint sounds like a taste issue, so the first thing I did was measure it. Most of
the current palette is fine — but one pair is not, and it is the most prominent control on
the page:

| pair | ratio | verdict |
| --- | --- | --- |
| **white `#ffffff` on the coral button `#FF6961`** | **2.82:1** | **FAILS AA** (needs 4.5:1 for normal text) |
| body text `#e8eaf6` on card | 12.43:1 | AAA |
| secondary grey `#8892b0` on card | 4.81:1 | AA (just clears) |
| error red on card | 5.28:1 | AA |
| success green on card | 6.67:1 | AA |

So the current theme **passes AA for body text** — meaning the reported problem is **not**
an insufficient-contrast-ratio problem in the body copy. It is two separate things:

1. **A real WCAG failure on the primary button.** White on this coral is 2.82:1. That is
   the element a user is most likely to squint at, and it is the one thing here that is
   objectively wrong rather than a preference.
2. **Glare behaviour.** On a glossy panel, light-on-dark text loses apparent weight to
   reflection and to subpixel anti-aliasing far more than dark-on-light does. A ratio of
   4.81:1 for the secondary grey is *compliant* and still reads thin under reflection —
   which is consistent with a complaint that is real but not captured by the ratio alone.

Reporting both matters: fixing only the ratio would leave the user's actual complaint
unaddressed, and "improving contrast" without measuring would have missed the button.

## The mockups

Same layout in all four, so the comparison is about colour and not structure.

| file | what it is |
| --- | --- |
| `1-current.png` | the shipped palette, for reference |
| `2-dark-lifted.png` | keeps the dark identity; lifts the secondary greys and the semantic colours |
| `3-light.png` | light surfaces — the best answer to glare specifically |
| `4-dark-grounded.png` | dark, but with a near-black ground so the same greys gain ratio without being changed |

Measured (secondary text on card, and the button label):

| variant | body | secondary | button label |
| --- | --- | --- | --- |
| 1 current | 12.43:1 | 4.81:1 | **2.82:1 ✗** |
| 2 dark-lifted | 13.65:1 | 8.27:1 | 7.91:1 ✓ (dark label) |
| 3 light | 15.73:1 | 6.66:1 | 6.54:1 ✓ (white label) |
| 4 dark-grounded | 15.28:1 | 8.24:1 | 8.13:1 ✓ (dark label) |

Note the button fix differs by theme: on a **light** coral (dark themes) the label must be
**dark**; on a **dark** red (light theme) it must be **white**. Keeping white on a light
coral is the bug that is shipped today.

## Recommendation

**4 (dark-grounded)** if the dark brand identity should be kept — it fixes the button,
lifts the secondary text clear of the AA floor with margin, and is the smallest visual
change that addresses glare. **3 (light)** if the glare complaint is the priority, since a
bright ground genuinely suppresses reflections better; it is the bigger departure from the
current brand.

I would not ship **2** over **4**: they are equivalent on contrast, and 4 gets there with a
darker ground rather than lighter greys, which keeps more separation between the card and
the page.

## Reproducing

```
python3 contrast.py    # the WCAG table for every pair, all four palettes
python3 mock.py        # re-renders the four PNGs (SVG -> PNG via rsvg-convert)
```

`contrast.py` implements WCAG 2.1 relative luminance; the thresholds it checks are 4.5:1
(body), 3.0:1 (large text and UI borders) and 7.0:1 (AAA).

## Where the real theme lives

These mockups are **static renders**, not screenshots of the app. The admin and balance
pages are served from the **captive-portal repo** (`tollgate-captive-portal-site`), not this
one — the installer's own `index.html` is the wizard and is a separate surface. So applying
the chosen palette is a change in that repo, driven through the brand-token mechanism
(#153) rather than by editing colours in place.

**Not done here:** applying the palette, and the Playwright screenshot of the real pages
after the change. The card's acceptance asks for both; this PR is the design evidence they
depend on.
