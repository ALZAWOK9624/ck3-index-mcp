# GUI composition

Read for visual review after content and bindings are understood. Treat the following as design heuristics; project art direction and verified readability decide the result.

- Use a small, meaningful spacing scale. Related labels and values should sit closer than unrelated rows. Check actual rendered alignment when fixed sizes, expansion, and margins mix.
- Build a clear hierarchy with typography, weight, color, spacing, and grouping. There is no universal maximum of three sizes or spacing values.
- Evaluate contrast on the composited background, including masks and alpha. A vanilla text color designed for a dark panel may fail on parchment. Check disabled states and tooltips as well as normal body text.
- Keep color meanings consistent and pair important color signals with text or shapes. Several faction colors can be useful when they identify different entities.
- Put decoration where it supports the intended theme without obscuring controls or content. Reuse suitable native controls, but do not ban custom interactions when the task needs them.
- Test long and short localized values, intended languages, and relevant UI scales. Pixel size alone cannot establish legibility across fonts, displays, and scaling.
- Check empty lists, maximum values, hidden rows, hover, disabled buttons, scrolling, and resized content. Watch where unused space accumulates.

Render an explicit review scenario with `ck3_gui operation=preview`, inspect the image/HTML, then adjust concrete issues. Stop when the intended states are readable and the known preview limitations have been separated from actual layout faults; a mandatory number of revision rounds does not improve correctness.
