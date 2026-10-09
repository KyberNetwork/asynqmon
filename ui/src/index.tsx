// initFlags must be imported first: it parses the server-injected window flags
// as a side effect, and the store/api modules read those values at import time.
import "./initFlags";
import React from "react";
import ReactDOM from "react-dom";
import CssBaseline from "@material-ui/core/CssBaseline";
import { Provider } from "react-redux";
import App from "./App";
import store from "./store";
import * as serviceWorker from "./serviceWorker";
import { saveState } from "./localStorage";
import { SettingsState } from "./reducers/settingsReducer";

let currentSettings: SettingsState | undefined = undefined;
let currentInstanceId: string | undefined = undefined;
store.subscribe(() => {
  const prevSettings = currentSettings;
  const prevInstanceId = currentInstanceId;
  currentSettings = store.getState().settings;
  currentInstanceId = store.getState().instances.selectedId;

  // Write to local-storage only when settings or the selected instance change.
  if (prevSettings !== currentSettings || prevInstanceId !== currentInstanceId) {
    saveState(store.getState());
  }
});

ReactDOM.render(
  <React.StrictMode>
    <CssBaseline />
    <Provider store={store}>
      <App />
    </Provider>
  </React.StrictMode>,
  document.getElementById("root")
);

// If you want your app to work offline and load faster, you can change
// unregister() to register() below. Note this comes with some pitfalls.
// Learn more about service workers: https://bit.ly/CRA-PWA
// TODO(hibiken): Look into this.
serviceWorker.unregister();
