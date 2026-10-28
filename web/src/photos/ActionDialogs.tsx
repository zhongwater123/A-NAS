import { AlbumPicker } from "./AlbumPicker";
import type { Caller, Place } from "./model";
import { PeopleSettings, PersonPicker } from "./People";
import { PhotoPicker } from "./PhotoPicker";
import type { PhotoActions } from "./usePhotoActions";

// ActionDialogs are the dialogs the actions on photos open over the window:
// the person to merge into, the album to add photos to, the photos to add to
// an album, and the libraries' people and pets switches.
export function ActionDialogs({ place, caller, actions }: { place: Place; caller: Caller; actions: PhotoActions }) {
  const {
    merging,
    setMerging,
    mergeInto,
    picker,
    setPicker,
    addTo,
    createAndAdd,
    photoPicker,
    setPhotoPicker,
    peopleSettings,
    setPeopleSettings,
    setPeople,
  } = actions;
  return (
    <>
      {merging && (
        <PersonPicker
          person={merging}
          viewing={place.kind === "person" ? place.viewing : ""}
          onPick={(into) => void mergeInto(into)}
          onClose={() => setMerging(undefined)}
        />
      )}
      {picker && (
        <AlbumPicker
          caller={caller}
          assets={picker}
          onPick={(album) => void addTo(album, picker)}
          onCreate={(library, name) => void createAndAdd(library, name, picker)}
          onClose={() => setPicker(undefined)}
        />
      )}
      {photoPicker && (
        <PhotoPicker
          album={photoPicker}
          onAdd={(assets) => {
            setPhotoPicker(undefined);
            void addTo(photoPicker, assets);
          }}
          onClose={() => setPhotoPicker(undefined)}
        />
      )}
      {peopleSettings && (
        <PeopleSettings
          caller={caller}
          onSet={(library, on) => void setPeople(library, on)}
          onClose={() => setPeopleSettings(false)}
        />
      )}
    </>
  );
}
