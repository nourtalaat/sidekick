# Shared functions of scripts/notice.sh and scripts/validator-notice.sh,
# which source this file.

rule='================================================================================'

# has_apache reports whether a license file holds the Apache License 2.0
# text, which starts with its centered title line.
has_apache() {
	grep -Eq '^[[:space:]]*Apache License[[:space:]]*$' "$1" &&
		grep -q 'Version 2.0, January 2004' "$1"
}

# has_other reports whether a license file also holds another license.
has_other() {
	grep -q 'Permission is hereby granted' "$1" || grep -q 'Redistribution and use' "$1"
}

# copyrights prints the filled-in copyright lines of a license file, leaving
# out the template line of the Apache appendix.
copyrights() {
	grep -E '^[[:space:]]*Copyright ' "$1" | grep -v -e '\[yyyy\]' -e '{yyyy}' | sed 's/^[[:space:]]*//' || true
}

# without_apache prints a license file that holds the Apache License 2.0
# text and another license, with the Apache text left out: from its title
# to "END OF TERMS AND CONDITIONS", and its appendix, if one follows.
without_apache() {
	awk '
		state == "before" && /^[[:space:]]*Apache License[[:space:]]*$/ {
			print "[The Apache License, Version 2.0 text, as in LICENSE, is left out here.]"
			state = "terms"
			next
		}
		state == "terms" && /END OF TERMS AND CONDITIONS/ { state = "after-terms"; next }
		state == "terms" { next }
		state == "after-terms" && /^[[:space:]]*$/ { next }
		state == "after-terms" && /APPENDIX: How to apply/ { state = "appendix"; next }
		state == "after-terms" { state = "after"; print ""; print; next }
		state == "appendix" && /limitations under the License\./ { state = "after"; next }
		state == "appendix" { next }
		{ print }
	' state=before "$1"
}

# section prints one component: its name, then each license and notice file
# in dir.
section() {
	name=$1
	dir=$2

	printf '\n%s\n%s\n%s\n' "$rule" "$name" "$rule"

	for file in $(find "$dir/" -maxdepth 1 -type f \( -iname 'licen[cs]e*' -o -iname 'copying*' -o -iname 'notice*' \) | LC_ALL=C sort); do
		base=$(basename "$file")

		case "$base" in
		[Nn][Oo][Tt][Ii][Cc][Ee]*)
			printf '\n%s:\n\n' "$base"
			cat "$file"
			;;
		*)
			if has_apache "$file" && ! has_other "$file"; then
				printf '\n%s: Apache License, Version 2.0 (text in LICENSE)\n' "$base"
				copyrights "$file"
			elif has_apache "$file"; then
				printf '\n%s:\n\n' "$base"
				without_apache "$file"
			else
				printf '\n%s:\n\n' "$base"
				cat "$file"
			fi
			;;
		esac
	done
}

# Some Go installs, such as Homebrew's, leave LICENSE out of GOROOT, so the
# Go project's license is written out here.
go_license() {
	printf '\n%s\n%s\n%s\n\nLICENSE:\n\n' "$rule" "Go standard library and runtime" "$rule"
	cat <<'EOF'
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
EOF
}

# go_module_section prints one Go module, given as path@version: its
# license and notice files from the module cache, downloaded first when
# needed, which checks the module against the checksum database.
# It returns 1, printing nothing, when the module cannot be downloaded.
go_module_section() {
	json=$(go mod download -json "$1") || return 1
	dir=$(echo "$json" | sed -n 's/^[[:space:]]*"Dir": "\(.*\)",$/\1/p')
	if [ -z "$dir" ]; then
		return 1
	fi

	section "$(echo "$1" | sed 's/@/ /')" "$dir"
}
