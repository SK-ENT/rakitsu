**Import Report**  

- **Row 1**: Accepted.  
  - **Reason**: Valid data (unique ID, active status, required country).  

- **Row 2**: Quarantined.  
  - **Reason**: Invalid status ("archived" is not allowed; must be "active" or "paused").  

- **Row 3**: Quarantined.  
  - **Reason**: Duplicate ID ("15" already exists in Row 1).  

- **Row 4**: Quarantined.  
  - **Reason**: Missing required country field.
