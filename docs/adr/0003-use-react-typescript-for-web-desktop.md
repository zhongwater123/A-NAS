# Use React and TypeScript for the Web desktop

A-NAS will build its browser desktop with React, TypeScript, Vite, and npm-locked dependencies. The desktop interaction model already requires multiple windows, asynchronous host state, and explicit failure states; typed state and component-level tests provide better locality than extending the original single-file prototype. Vite output is generated before Go compilation and embedded in `anas-api`, so the Experimental NAS runs one versioned binary and does not require Node.js.

The preserved prototype is a primary design source at [`86b034b`](https://github.com/zhongwater123/A-NAS/blob/86b034bb410d540dfd681c5be5af91d8fd00afb7/prototype/ai-home-nas-desktop-prototype.html), not production code. The first production slice keeps its desktop and window language while exposing only Resource Management and System Settings; other launchers remain visibly unavailable until their behavior exists.
