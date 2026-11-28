`arcdatastaging` is written from the arc_data_staging submit command, which this
parser does not implement: only the C++ side writes the attribute.

SetGridParams reads four ARC commands in one block -- arc_rte, arc_resources,
arc_application and arc_data_staging -- each a plain submit_param whose value
goes straight onto the ad. This library implements the first two and not the
last two.

Only arc_rte has a row in prunable_keywords. The other three are read directly,
so until nonTableKeywords seeded them by hand the generator could not write any
of them and nothing compared them: the gap was invisible rather than known.

Implementing it is two lines beside the arc_resources that is already there,
and is its own change.
