# Film kit — content validation gate.
#
#   make film-verify FILMS="/path/a.mp4 /path/b.mp4"
#
# Fails on any black frame segment, any error string on screen, or a missing
# required happy-path string. See README.md.

FILMS ?= $(wildcard *.mp4)

.PHONY: film-verify
film-verify:
	@test -n "$(FILMS)" || { echo "no films (set FILMS=...)"; exit 2; }
	python3 film-verify.py $(FILMS)
