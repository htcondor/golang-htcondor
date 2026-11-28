`outputdirectory` is the job's output directory, which HTCondor resolves to a
full path and this parser does not write at all.

HTCONDOR-3618 added the organized output submit commands -- organized_output,
organized_output_directory and organized_output_pattern -- and made
output_directory f_special in the same change, because the two interact.
SetOutputDirSpecial picks between them, expands the pattern, checks that it
stays below the base directory, resolves the result against the job's initial
working directory and assigns OutputDirectory itself.

The Go parser has none of that. Before the change it carried output_directory
as an ordinary table row, which wrote the value as it was spelled rather than
as a full path, and the generator never produced a case that told the two
apart. The organized output keywords gave it one: a base directory alone turns
organized output on, the pattern comes from ORGANIZED_OUTPUT_PATTERN, and C++
writes an attribute Go has no keyword for.

The output_directory row stays in the simple table -- see
keywordsSpecialInOracle -- so a submit file naming it still writes the
attribute, from the value as it was spelled. What is missing is everything
SetOutputDirSpecial does on top: the organized output keywords have no row at
all, and no path is resolved against Iwd.

Implementing it is a separate change: it means porting SetOutputDirSpecial and
giving the f_genfile role meaning in the simple pass, where today it is carried
in the table and read by nothing.

Oracle: has HTCONDOR-3618
