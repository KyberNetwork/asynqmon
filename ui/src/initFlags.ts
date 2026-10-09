// This module parses the server-injected window flags as a side effect on
// import. It MUST be imported before any module that reads the parsed values at
// evaluation time (e.g. store/reducers/api read window.INSTANCES), so keep it as
// the first import in index.tsx.
import parseFlagsUnderWindow from "./parseFlags";

parseFlagsUnderWindow();
