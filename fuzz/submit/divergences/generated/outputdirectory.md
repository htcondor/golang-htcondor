`outputdirectory` is the job's output directory. Against an oracle that has
HTCONDOR-3618 the two sides agree; against one that predates it only Go writes
the attribute.

HTCONDOR-3618 added the organized output commands -- organized_output,
organized_output_directory and organized_output_pattern -- which build an
output directory per job from a base directory and a pattern. This parser
implements them, so naming any of them writes OutputDirectory here. An older
HTCondor has no such commands: it reads the names as ordinary submit macros,
writes nothing, and the attribute appears on one side only.

Parsing them is not gated. A submit file naming organized_output_directory
means the same thing wherever it is submitted, and an attribute an older
schedd ignores costs the job nothing -- the same reasoning as
keywordsAheadOfOracle.

Note that output_directory itself is NOT the divergence. It stays an ordinary
f_genfile row in the keyword table, which has always resolved it against Iwd
the way SetOutputDirSpecial does, so it agrees against either oracle. What
upstream changed for it was only which pass writes it, and that is recorded in
keywordsSpecialInOracle rather than here.

Sweep cannot prove this: the generator writes organized_output_directory only when the oracle's keyword table carries it, so against an older HTCondor the sweep sees output_directory alone, which agrees. TestOrganizedOutputMatchesOracle compares the organized output shapes directly, against either oracle.
Oracle: lacks HTCONDOR-3618
