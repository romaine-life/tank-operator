!macro setSilentIfOption option
  ClearErrors
  ${GetOptions} $R0 "${option}" $R1
  ${IfNot} ${Errors}
    SetSilent silent
  ${EndIf}
!macroend

!macro enableUnattendedAliases
  ${GetParameters} $R0
  !insertmacro setSilentIfOption "/unattended"
  !insertmacro setSilentIfOption "/quiet"
  !insertmacro setSilentIfOption "/silent"
  !insertmacro setSilentIfOption "--unattended"
  !insertmacro setSilentIfOption "--quiet"
  !insertmacro setSilentIfOption "--silent"
!macroend

!macro customInit
  !insertmacro enableUnattendedAliases
!macroend

!macro customUnInit
  !insertmacro enableUnattendedAliases
!macroend
