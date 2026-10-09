import { initialState as settingsInitialState } from "./reducers/settingsReducer"
import { initialState as instancesInitialState } from "./reducers/instancesReducer"
import { setCurrentInstanceId } from "./api";
import { AppState } from "./store";

const LOCAL_STORAGE_KEY = "asynqmon:state";

export function loadState(): Partial<AppState> {
  try {
    const serializedState = localStorage.getItem(LOCAL_STORAGE_KEY);
    if (serializedState === null) {
      return {};
    }
    const savedState = JSON.parse(serializedState);

    // Restore the previously selected instance only if it still exists in the
    // current server-provided list; otherwise fall back to the default.
    const savedInstanceId: string | undefined = savedState.instances?.selectedId;
    const selectedId =
      savedInstanceId &&
      instancesInitialState.available.some((i) => i.id === savedInstanceId)
        ? savedInstanceId
        : instancesInitialState.selectedId;
    // Keep the api module in sync with the restored selection.
    setCurrentInstanceId(selectedId);

    return {
      settings: {
        ...settingsInitialState,
        ...(savedState.settings || {}),
      },
      instances: {
        ...instancesInitialState,
        selectedId,
      },
    }
  } catch (err) {
    console.log("loadState: could not load state ", err)
    return {};
  }
}

export function saveState(state: AppState) {
  try {
    const serializedState = JSON.stringify({
      settings: state.settings,
      instances: { selectedId: state.instances.selectedId },
    });
    localStorage.setItem(LOCAL_STORAGE_KEY, serializedState);
  } catch (err) {
    console.error("saveState: could not save state: ", err);
  }
}
