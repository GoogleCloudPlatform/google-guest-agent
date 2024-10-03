#!/bin/bash

# This script is used to generate the rpm spec file for the guest agent.
# The scripts creates an adaptation layer between the publicly visible rpm spec
# and the internal genrpm spec.
#
# The adaptation layer allows us to use blaze built artifacts rather than having'
# the rpmbuild process actually build the artifacts - at the same time that the
# same spec file can be used for both internal and external builds.
#
# We introduce a prebuilt variable exposed to the rpm spec so that one can guard
# parts of the spec file that are not relevant for the internal build.
#
# It takes two arguments:
#
#   1. The template file to use (the .spec file in the packaging directory in
#.     the codebase);
#   2. The output file to write to;
#
# The template file will have the following replacements made:
#   1. A header that adds the genrpm's required macro add (they are applied so
#      we can use blaze's built artifacts - rather than having the rpmbuild
#      process actually building the artifacts)
#   2. A footer that includes the %install and %files section (from genrpm's
#      macro)
#
# Example usage:
#   ./gen-rpm-spec.sh template.spec output-gen.spec

set -e

template=$1
output=$2

if [ ! -f $template ]; then
  echo "Template file not found: $template"
  exit 1
fi

if [ ! -d $(dirname $output) ]; then
  echo "Output directory found: $output"
  exit 1
fi

header="
%include %build_rpm_options
%define prebuilt 1
"

footer="
%install
%include %build_rpm_install

%files
%include %build_rpm_files
"

echo "${header}" >> $output
cat $template >> $output
echo "${footer}" >> $output